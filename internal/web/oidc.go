package web

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"

	"github.com/juev/freshgo/internal/store"
)

const (
	// oidcCookie holds what a sign-in through the provider has to find
	// again when the browser comes back from there.
	oidcCookie = "freshgo_oidc"
	// oidcLife is how long the visitor has to sign in at the provider.
	oidcLife = 10 * time.Minute
	// oidcCallbackPath is where the provider sends the browser back to; it
	// is the address a client is registered with there.
	oidcCallbackPath = "/oidc/callback"
	// maxOIDCNext is the longest address to land on that a sign-in carries
	// through the provider, in bytes; a longer one lands on the start page.
	maxOIDCNext = 2048
	// oidcTimeout bounds one request to the provider.
	oidcTimeout = 15 * time.Second
)

// oidcAttempt is a sign-in on its way through the provider.
type oidcAttempt struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
}

// oidcProviders keeps what the provider says about itself, read once for an
// issuer. Sign-ins that find it unread wait for one reading, not for each
// other.
type oidcProviders struct {
	mu       sync.RWMutex
	issuer   string
	provider *oidc.Provider
	client   *http.Client
	reading  singleflight.Group
}

// get returns what the issuer says about itself, asking it when it is not
// known yet; sign-ins that ask at once share one request.
func (p *oidcProviders) get(ctx context.Context, issuer string) (*oidc.Provider, error) {
	p.mu.RLock()
	provider := p.provider
	if p.issuer != issuer {
		provider = nil
	}
	p.mu.RUnlock()
	if provider != nil {
		return provider, nil
	}
	read, err, _ := p.reading.Do(issuer, func() (any, error) {
		// The request is shared: one sign-in that gives up does not end
		// it for the others. The client bounds it.
		provider, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), p.client), issuer)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.issuer, p.provider = issuer, provider
		p.mu.Unlock()
		return provider, nil
	})
	if err != nil {
		return nil, err
	}
	return read.(*oidc.Provider), nil
}

// errOIDC is a sign-in the provider or its answer did not carry through.
var errOIDC = errors.New("web: sign-in through the provider failed")

// oidcOn reports whether visitors can sign in through a provider: next to
// the login by password, with a provider named in the settings and the
// secret of the client given to the server.
func (h *Handler) oidcOn(system store.System) bool {
	return system.AuthType == store.AuthForm && h.oidcSecret != "" && system.OIDC.Issuer != "" && system.OIDC.ClientID != ""
}

// oidcName is what the provider is called on the pages: the host it is at.
func oidcName(system store.System) string {
	if u, err := url.Parse(system.OIDC.Issuer); err == nil && u.Host != "" {
		return u.Host
	}
	return system.OIDC.Issuer
}

// oidcStart is the address a sign-in through the provider starts at.
func (h *Handler) oidcStart(next string) string {
	return h.url("/oidc/login") + "?" + url.Values{"next": {next}}.Encode()
}

// oidcClient returns the client of the provider of the settings and what
// checks its tokens. ctx carries the HTTP client the provider is asked with.
func (h *Handler) oidcClient(r *http.Request, system store.System) (context.Context, *oauth2.Config, *oidc.IDTokenVerifier, error) {
	provider, err := h.oidc.get(r.Context(), system.OIDC.Issuer)
	if err != nil {
		return nil, nil, nil, err
	}
	config := &oauth2.Config{
		ClientID: system.OIDC.ClientID, ClientSecret: h.oidcSecret,
		Endpoint: provider.Endpoint(), RedirectURL: h.absolute(r, oidcCallbackPath),
		Scopes: []string{oidc.ScopeOpenID, "profile"},
	}
	return oidc.ClientContext(r.Context(), h.oidc.client), config, provider.Verifier(&oidc.Config{ClientID: system.OIDC.ClientID}), nil
}

// setOIDCAttempt hands the browser a sign-in to bring back from the
// provider, or takes it away when attempt is nil.
func (h *Handler) setOIDCAttempt(w http.ResponseWriter, r *http.Request, attempt *oidcAttempt) error {
	cookie := &http.Cookie{
		Name: oidcCookie, Path: h.prefix + "/", MaxAge: -1,
		// Lax: the browser comes back by a link of another site.
		HttpOnly: true, Secure: h.secure(r), SameSite: http.SameSiteLaxMode,
	}
	if attempt != nil {
		raw, err := json.Marshal(attempt)
		if err != nil {
			return err
		}
		cookie.Value, cookie.MaxAge = base64.RawURLEncoding.EncodeToString(raw), int(oidcLife/time.Second)
	}
	http.SetCookie(w, cookie)
	return nil
}

// oidcLogin sends the visitor to the provider to sign in.
func (h *Handler) oidcLogin(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	if !h.oidcOn(s.system) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	// What is kept in the cookie has to fit into one.
	next := r.URL.Query().Get("next")
	if len(next) > maxOIDCNext {
		next = ""
	}
	_, config, _, err := h.oidcClient(r, s.system)
	if err != nil {
		h.oidcRefused(w, r, http.StatusBadGateway, next, err)
		return
	}
	attempt := &oidcAttempt{Verifier: oauth2.GenerateVerifier(), Next: next}
	if attempt.State, err = newToken(); err == nil {
		attempt.Nonce, err = newToken()
	}
	if err == nil {
		err = h.setOIDCAttempt(w, r, attempt)
	}
	if err != nil {
		h.broken(w, r, err)
		return
	}
	http.Redirect(w, r, config.AuthCodeURL(attempt.State, oidc.Nonce(attempt.Nonce), oauth2.S256ChallengeOption(attempt.Verifier)), http.StatusSeeOther)
}

// oidcCallback takes the visitor back from the provider and logs in the
// user the provider names.
func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	if !h.oidcOn(s.system) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	// An attempt is good for one answer.
	var attempt oidcAttempt
	if cookie, err := r.Cookie(oidcCookie); err == nil {
		if raw, err := base64.RawURLEncoding.DecodeString(cookie.Value); err == nil {
			_ = json.Unmarshal(raw, &attempt)
		}
	}
	if err := h.setOIDCAttempt(w, r, nil); err != nil {
		h.broken(w, r, err)
		return
	}
	query := r.URL.Query()
	if attempt.State == "" || subtle.ConstantTimeCompare([]byte(attempt.State), []byte(query.Get("state"))) != 1 {
		h.oidcRefused(w, r, http.StatusBadRequest, attempt.Next, fmt.Errorf("%w: the answer belongs to no sign-in of this browser", errOIDC))
		return
	}
	if problem := query.Get("error"); problem != "" {
		h.oidcRefused(w, r, http.StatusForbidden, attempt.Next, fmt.Errorf("%w: the provider answered %q", errOIDC, problem))
		return
	}
	identity, err := h.oidcIdentity(r, s.system, attempt, query.Get("code"))
	if err != nil {
		h.oidcRefused(w, r, http.StatusBadGateway, attempt.Next, err)
		return
	}
	user, err := h.oidcUser(r.Context(), s.system, identity)
	if errors.Is(err, errOIDC) {
		h.oidcRefused(w, r, http.StatusForbidden, attempt.Next, err)
		return
	}
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if !readPreferences(user).enabled() {
		h.oidcRefused(w, r, http.StatusForbidden, attempt.Next, fmt.Errorf("%w: user %q is disabled", errOIDC, user.Name))
		return
	}
	// The login the browser had ends: who signs in now may be another user.
	if s.who != nil && s.who.session != nil {
		if err := h.db.DeleteSession(r.Context(), s.who.session.TokenHash); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	if err := h.startSession(w, r, user, false); err != nil {
		h.broken(w, r, err)
		return
	}
	http.Redirect(w, r, h.localTarget(attempt.Next), http.StatusSeeOther)
}

// oidcClaims is who the ID token says signs in.
type oidcClaims struct {
	// Issuer and Subject are who the user is at the provider; Name is the
	// name a new user takes.
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
	Name    string `json:"preferred_username"`
	// Groups are the groups the provider puts the user in, a list or a
	// single name.
	Groups json.RawMessage `json:"groups"`
}

// inGroup reports whether the claims put the user in the group.
func (c oidcClaims) inGroup(group string) bool {
	var groups []string
	if json.Unmarshal(c.Groups, &groups) != nil {
		var one string
		if json.Unmarshal(c.Groups, &one) != nil {
			return false
		}
		groups = []string{one}
	}
	return slices.Contains(groups, group)
}

// oidcIdentity trades the code of the provider for its tokens and returns
// what the ID token says about the user.
func (h *Handler) oidcIdentity(r *http.Request, system store.System, attempt oidcAttempt, code string) (oidcClaims, error) {
	var claims oidcClaims
	ctx, config, verifier, err := h.oidcClient(r, system)
	if err != nil {
		return claims, err
	}
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		return claims, err
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return claims, fmt.Errorf("%w: the provider gave no ID token", errOIDC)
	}
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return claims, err
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(attempt.Nonce)) != 1 {
		return claims, fmt.Errorf("%w: the ID token belongs to another sign-in", errOIDC)
	}
	if err := idToken.Claims(&claims); err != nil {
		return claims, err
	}
	claims.Issuer, claims.Subject = idToken.Issuer, idToken.Subject
	if claims.Subject == "" {
		return claims, fmt.Errorf("%w: the ID token names no subject", errOIDC)
	}
	return claims, nil
}

// oidcUser returns the user the claims are of: the one the subject belongs
// to. A subject nobody has yet goes to the user named preferred_username,
// made if need be, unless that user already has another subject at the
// provider. With an administrators' group set, the user is an administrator
// exactly when the provider puts them in it. An error that wraps errOIDC
// is a user who may not sign in.
func (h *Handler) oidcUser(ctx context.Context, system store.System, claims oidcClaims) (*store.User, error) {
	var user *store.User
	id, err := h.db.OIDCUser(ctx, claims.Issuer, claims.Subject)
	switch {
	case err == nil:
		if user, err = h.db.UserByID(ctx, id); err != nil {
			return nil, err
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	default:
		if !store.ValidUserName(claims.Name) {
			return nil, fmt.Errorf("%w: subject %q has no name a user can have", errOIDC, claims.Subject)
		}
		if user, err = h.userOrNew(ctx, claims.Name, system, "user created on the word of the provider"); err != nil {
			return nil, err
		}
		if user == nil {
			return nil, fmt.Errorf("%w: no user %q", errOIDC, claims.Name)
		}
		switch err := h.db.BindOIDC(ctx, claims.Issuer, claims.Subject, user.ID); {
		case errors.Is(err, store.ErrConflict):
			// Another sign-in of the same subject may have been quicker.
			if id, err := h.db.OIDCUser(ctx, claims.Issuer, claims.Subject); err == nil && id == user.ID {
				break
			}
			return nil, fmt.Errorf("%w: user %q is another subject at the provider", errOIDC, user.Name)
		case err != nil:
			return nil, err
		}
		h.log.Info("user bound to the provider", "user", user.Name, "subject", claims.Subject)
	}
	if group := system.OIDC.AdminGroup; group != "" {
		admin := claims.inGroup(group)
		if readPreferences(user).IsAdmin != admin {
			err := h.db.UpdateUserSettings(ctx, user.ID, func(settings map[string]json.RawMessage) error {
				settings["is_admin"], _ = json.Marshal(admin)
				return nil
			})
			if err != nil {
				return nil, err
			}
			h.log.Info("administration changed on the word of the provider", "user", user.Name, "admin", admin)
			if user, err = h.db.UserByID(ctx, user.ID); err != nil {
				return nil, err
			}
		}
	}
	return user, nil
}

// oidcRefused shows the login page with the message of a sign-in through
// the provider that did not work. Why is for the log: the visitor is told
// the same whatever it was.
func (h *Handler) oidcRefused(w http.ResponseWriter, r *http.Request, status int, next string, why error) {
	h.log.Warn("sign-in through the provider refused", "provider", state(r).system.OIDC.Issuer, "error", why)
	v := h.view(r, "login", "login.heading")
	h.showLogin(w, r, status, loginForm{Next: next, Error: v.T("login.oidc.failed")})
}
