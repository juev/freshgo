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
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

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
// issuer.
type oidcProviders struct {
	mu       sync.Mutex
	issuer   string
	provider *oidc.Provider
	client   *http.Client
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
	p := &h.oidc
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx := oidc.ClientContext(r.Context(), p.client)
	if p.provider == nil || p.issuer != system.OIDC.Issuer {
		provider, err := oidc.NewProvider(ctx, system.OIDC.Issuer)
		if err != nil {
			return nil, nil, nil, err
		}
		p.issuer, p.provider = system.OIDC.Issuer, provider
	}
	config := &oauth2.Config{
		ClientID: system.OIDC.ClientID, ClientSecret: h.oidcSecret,
		Endpoint: p.provider.Endpoint(), RedirectURL: h.absolute(r, oidcCallbackPath),
		Scopes: []string{oidc.ScopeOpenID, "profile"},
	}
	return ctx, config, p.provider.Verifier(&oidc.Config{ClientID: system.OIDC.ClientID}), nil
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
	name, err := h.oidcUserName(r, s.system, attempt, query.Get("code"))
	if err != nil {
		h.oidcRefused(w, r, http.StatusBadGateway, attempt.Next, err)
		return
	}
	var user *store.User
	if store.ValidUserName(name) {
		if user, err = h.userOrNew(r.Context(), name, s.system, "user created on the word of the provider"); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	if user == nil || !readPreferences(user).enabled() {
		h.oidcRefused(w, r, http.StatusForbidden, attempt.Next, fmt.Errorf("%w: no user %q may sign in", errOIDC, name))
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

// oidcUserName trades the code of the provider for its tokens and returns
// the name the ID token gives the user.
func (h *Handler) oidcUserName(r *http.Request, system store.System, attempt oidcAttempt, code string) (string, error) {
	ctx, config, verifier, err := h.oidcClient(r, system)
	if err != nil {
		return "", err
	}
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		return "", err
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return "", fmt.Errorf("%w: the provider gave no ID token", errOIDC)
	}
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(attempt.Nonce)) != 1 {
		return "", fmt.Errorf("%w: the ID token belongs to another sign-in", errOIDC)
	}
	var claims struct {
		Name string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", err
	}
	return claims.Name, nil
}

// oidcRefused shows the login page with the message of a sign-in through
// the provider that did not work. Why is for the log: the visitor is told
// the same whatever it was.
func (h *Handler) oidcRefused(w http.ResponseWriter, r *http.Request, status int, next string, why error) {
	h.log.Warn("sign-in through the provider refused", "provider", state(r).system.OIDC.Issuer, "error", why)
	v := h.view(r, "login", "login.heading")
	h.showLogin(w, r, status, loginForm{Next: next, Error: v.T("login.oidc.failed")})
}
