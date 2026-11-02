package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/juev/freshgo/internal/store"
)

const (
	// passkeyCookie holds the secret that finds the ceremony of a browser
	// again when the authenticator has answered.
	passkeyCookie = "freshgo_passkey"
	// passkeyLife is how long the user has to answer the authenticator.
	passkeyLife = 5 * time.Minute
	// knownCeremonies bounds the ceremonies that wait for an answer.
	knownCeremonies = 10000
	// maxPasskeyBody bounds what a browser sends about a ceremony, in bytes.
	maxPasskeyBody = 64 << 10
	// maxPasskeyName is the longest name of a passkey, in characters.
	maxPasskeyName = 64
	// passkeyHandleSize is the size of the user handle, the longest the
	// standard allows.
	passkeyHandleSize = 64
)

// Where the script of the profile begins and finishes the registration of a
// passkey.
const (
	passkeyBeginPath  = "/settings/profile/passkeys/begin"
	passkeyFinishPath = "/settings/profile/passkeys/finish"
)

// errNoOrigin says the public address of the server is not one passkeys can
// be bound to.
var errNoOrigin = errors.New("web: passkeys need a public address over HTTPS")

// passkeyOrigin returns the origin browsers reach the server at and the
// relying party ID derived from it, from the public address; ok is false
// where a browser would not run a ceremony: without an address, over plain
// HTTP (except on localhost, which browsers treat as secure), or at an IP
// address, which cannot be a relying party ID.
func passkeyOrigin(baseURL string) (origin, rpID string, ok bool) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil {
		return "", "", false
	}
	if u.Scheme != "https" && (u.Scheme != "http" || host != "localhost") {
		return "", "", false
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), host, true
}

// passkeysOn reports whether users can have passkeys: they log in by a form
// and the server knows the origin its pages are served from.
func (h *Handler) passkeysOn(system store.System) bool {
	_, _, ok := passkeyOrigin(h.baseURL)
	return ok && system.AuthType == store.AuthForm
}

// relyingParty returns the server as the party passkeys are made for.
// Passkeys are bound to the origin of the public address: a page served
// from another one cannot use them, and neither can a phishing site.
func (h *Handler) relyingParty(system store.System) (*webauthn.WebAuthn, error) {
	origin, rpID, ok := passkeyOrigin(h.baseURL)
	if !ok {
		return nil, errNoOrigin
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyLife, TimeoutUVD: passkeyLife}
	return webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: system.Title, RPOrigins: []string{origin},
		// A passkey is a login of its own, so it has to be one the user
		// can pick without typing a name, and one that verifies the user.
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification: protocol.VerificationRequired,
		},
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts:              webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
}

// ceremonyKind is what a ceremony is for.
type ceremonyKind int

const (
	ceremonyRegister ceremonyKind = iota + 1
	ceremonyLogin
	ceremonyReauth
)

// ceremony is a challenge given to an authenticator whose answer is
// awaited.
type ceremony struct {
	kind    ceremonyKind
	session webauthn.SessionData
	// userID and login are who the ceremony is for and the login they have
	// it in, for a registration and a reauthentication; zero and empty for
	// a login, where the answer says who it is.
	userID int64
	login  string
	// name is what a registration names the passkey; next and persistent
	// are where a login lands and whether it outlives the browser.
	name, next string
	persistent bool
	expires    time.Time
}

// ceremonies keeps the challenges that wait for an answer, by the hash of
// the secret in the cookie of the browser they were given to. A challenge
// answers once: finding it takes it away, also for a wrong answer.
type ceremonies struct {
	mu    sync.Mutex
	known map[string]*ceremony
}

// add keeps a ceremony until its answer comes and returns the secret that
// finds it again.
func (c *ceremonies) add(cer *ceremony, now time.Time) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known == nil {
		c.known = map[string]*ceremony{}
	}
	if len(c.known) >= knownCeremonies {
		for key, old := range c.known {
			if !now.Before(old.expires) {
				delete(c.known, key)
			}
		}
		if len(c.known) >= knownCeremonies {
			clear(c.known)
		}
	}
	cer.expires = now.Add(passkeyLife)
	c.known[tokenHash(token)] = cer
	return token, nil
}

// take returns the ceremony of the secret and forgets it. A ceremony of
// another kind, or one that has run out, is not returned.
func (c *ceremonies) take(token string, kind ceremonyKind, now time.Time) (ceremony, bool) {
	if token == "" {
		return ceremony{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := tokenHash(token)
	cer := c.known[key]
	delete(c.known, key)
	if cer == nil || cer.kind != kind || !now.Before(cer.expires) {
		return ceremony{}, false
	}
	return *cer, true
}

// setCeremony hands the browser the secret of a ceremony. Strict: the
// answer is a request of the page itself.
func (h *Handler) setCeremony(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: passkeyCookie, Value: token, Path: h.prefix + "/", MaxAge: int(passkeyLife / time.Second),
		HttpOnly: true, Secure: h.secure(r), SameSite: http.SameSiteStrictMode,
	})
}

// takeCeremony returns the ceremony of the kind the browser holds the
// secret of, and takes the secret away from it.
func (h *Handler) takeCeremony(w http.ResponseWriter, r *http.Request, kind ceremonyKind) (ceremony, bool) {
	cookie, err := r.Cookie(passkeyCookie)
	if err != nil {
		return ceremony{}, false
	}
	http.SetCookie(w, &http.Cookie{
		Name: passkeyCookie, Path: h.prefix + "/", MaxAge: -1,
		HttpOnly: true, Secure: h.secure(r), SameSite: http.SameSiteStrictMode,
	})
	return h.ceremonies.take(cookie.Value, kind, h.now())
}

// startCeremony keeps a ceremony and hands the browser its secret.
func (h *Handler) startCeremony(w http.ResponseWriter, r *http.Request, cer *ceremony) bool {
	token, err := h.ceremonies.add(cer, h.now())
	if err != nil {
		h.broken(w, r, err)
		return false
	}
	h.setCeremony(w, r, token)
	return true
}

// answerJSON writes a value for the script.
func (h *Handler) answerJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// refuse answers the script with a status and nothing else: what went
// wrong is for the log.
func (h *Handler) refuse(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

// passkeyBody bounds and reads the JSON a script sends, into value; ok is
// false when the answer is given.
func (h *Handler) passkeyBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPasskeyBody)
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		h.refuse(w, http.StatusBadRequest)
		return false
	}
	return true
}

// passkeyUser is a user as the library knows them.
type passkeyUser struct {
	handle      []byte
	name        string
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// descriptors are the credentials of the user as an authenticator is told
// them.
func (u passkeyUser) descriptors() []protocol.CredentialDescriptor {
	return webauthn.Credentials(u.credentials).CredentialDescriptors()
}

// passkeyHandle returns the user handle of a user: 64 random bytes made at
// the first passkey and the same for every passkey after, so that the
// handle tells an authenticator which account a passkey belongs to without
// saying who the user is.
func (h *Handler) passkeyHandle(ctx context.Context, userID int64) ([]byte, error) {
	var handle string
	err := h.db.UpdateUserSettings(ctx, userID, func(settings map[string]json.RawMessage) error {
		if handle = attrs(settings).text("passkey_handle"); handle != "" {
			return nil
		}
		random := make([]byte, passkeyHandleSize)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		handle = base64.RawURLEncoding.EncodeToString(random)
		attrs(settings).set("passkey_handle", handle)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return base64.RawURLEncoding.DecodeString(handle)
}

// loadPasskeyUser returns a user with the credentials of their passkeys. The
// handle is made if the user has none yet and create is set; a user without
// one is otherwise no user with passkeys.
func (h *Handler) loadPasskeyUser(ctx context.Context, u *store.User, create bool) (passkeyUser, error) {
	user := passkeyUser{name: u.Name}
	passkeys, err := h.db.Passkeys(ctx, u.ID)
	if err != nil {
		return user, err
	}
	for _, p := range passkeys {
		var credential webauthn.Credential
		if err := json.Unmarshal(p.Credential, &credential); err != nil {
			return user, fmt.Errorf("web: passkey %d of user %q: %w", p.ID, u.Name, err)
		}
		user.credentials = append(user.credentials, credential)
	}
	if create {
		user.handle, err = h.passkeyHandle(ctx, u.ID)
		return user, err
	}
	if handle := readAttrs(u.Settings).text("passkey_handle"); handle != "" {
		user.handle, err = base64.RawURLEncoding.DecodeString(handle)
	}
	return user, err
}

// passkeyName returns the name of a passkey as the user wrote it, and
// whether it is one: some text without control characters.
func passkeyName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	ok := name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= maxPasskeyName &&
		strings.IndexFunc(name, unicode.IsControl) < 0
	return name, ok
}

// profileReauth is where a login that is too old to add a passkey is sent.
func (h *Handler) profileReauth() string {
	return h.url("/reauth") + "?next=" + url.QueryEscape("/settings/profile")
}

// passkeyContext is what the ceremonies of a request need: the party, and
// the user who asks. ok is false when the answer is given.
func (h *Handler) passkeyContext(w http.ResponseWriter, r *http.Request) (*webauthn.WebAuthn, bool) {
	s := state(r)
	if !h.passkeysOn(s.system) {
		h.refuse(w, http.StatusNotFound)
		return nil, false
	}
	rp, err := h.relyingParty(s.system)
	if err != nil {
		h.broken(w, r, err)
		return nil, false
	}
	return rp, true
}

// beginPasskey starts the registration of a passkey for the user who asks:
// it answers with the options for the authenticator. Whoever registers one
// has a login of their own for good, so the login that asks has to be a
// recent one, as for the administration.
func (h *Handler) beginPasskey(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	rp, ok := h.passkeyContext(w, r)
	if !ok {
		return
	}
	if h.stale(s) {
		h.answerJSON(w, r, http.StatusForbidden, map[string]string{"reauth": h.profileReauth()})
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !h.passkeyBody(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = h.view(r, "", "settings.passkeys").T("settings.passkeys.default-name")
	}
	name, ok = passkeyName(name)
	if !ok {
		h.refuse(w, http.StatusBadRequest)
		return
	}
	user, err := h.loadPasskeyUser(r.Context(), s.who.user, true)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	creation, session, err := rp.BeginRegistration(user,
		webauthn.WithExclusions(user.descriptors()),
		webauthn.WithExtensions(webauthn.WithExtensionCredProps()))
	if err != nil {
		h.broken(w, r, err)
		return
	}
	cer := &ceremony{kind: ceremonyRegister, session: *session, userID: s.who.user.ID, login: loginOf(s.who), name: name}
	if h.startCeremony(w, r, cer) {
		h.answerJSON(w, r, http.StatusOK, creation)
	}
}

// loginOf is what tells the login of a user from another of theirs.
func loginOf(who *identity) string {
	if who.session == nil {
		return ""
	}
	return who.session.TokenHash
}

// finishPasskey takes the answer of the authenticator and stores the
// passkey. The ceremony must be the one this browser began, for this user,
// in this login.
func (h *Handler) finishPasskey(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	rp, ok := h.passkeyContext(w, r)
	if !ok {
		return
	}
	cer, ok := h.takeCeremony(w, r, ceremonyRegister)
	if !ok || cer.userID != s.who.user.ID || cer.login != loginOf(s.who) {
		h.refuse(w, http.StatusBadRequest)
		return
	}
	if h.stale(s) {
		h.answerJSON(w, r, http.StatusForbidden, map[string]string{"reauth": h.profileReauth()})
		return
	}
	user, err := h.loadPasskeyUser(r.Context(), s.who.user, false)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPasskeyBody)
	credential, err := rp.FinishRegistration(user, cer.session, r)
	if err != nil {
		h.log.Warn("passkey not registered", "user", s.who.user.Name, "error", err)
		h.refuse(w, http.StatusBadRequest)
		return
	}
	// A passkey that cannot be picked without a name would never log in.
	if rk := credential.Extensions.RK; rk != nil && !*rk {
		h.log.Warn("passkey not registered: the authenticator made no discoverable credential", "user", s.who.user.Name)
		h.refuse(w, http.StatusBadRequest)
		return
	}
	data, err := json.Marshal(credential)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	err = h.db.AddPasskey(r.Context(), &store.Passkey{
		UserID: s.who.user.ID, CredentialID: credential.ID, Name: cer.name, Credential: data, Created: h.now().Unix(),
	})
	switch {
	case errors.Is(err, store.ErrConflict):
		h.refuse(w, http.StatusConflict)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.log.Info("passkey registered", "user", s.who.user.Name)
		h.notify(w, r, "notice.passkey-added", 0)
		w.WriteHeader(http.StatusNoContent)
	}
}

// passkeyRow is a passkey as the pages list it.
type passkeyRow struct {
	ID            int64
	Name          string
	Created, Used string
}

// passkeysPart is what a page shows of the passkeys of a user.
type passkeysPart struct {
	// Show says passkeys are part of how users log in here at all; On that
	// the server also knows an address to bind them to.
	Show, On bool
	Rows     []passkeyRow
}

// passkeysOf returns what a page shows of the passkeys of the user.
func (h *Handler) passkeysOf(ctx context.Context, system store.System, u *store.User) (passkeysPart, error) {
	part := passkeysPart{Show: system.AuthType == store.AuthForm, On: h.passkeysOn(system)}
	if !part.Show {
		return part, nil
	}
	passkeys, err := h.db.Passkeys(ctx, u.ID)
	if err != nil {
		return part, err
	}
	loc := readReading(u).location()
	for _, p := range passkeys {
		row := passkeyRow{ID: p.ID, Name: p.Name, Created: time.Unix(p.Created, 0).In(loc).Format("2006-01-02")}
		if p.Used > 0 {
			row.Used = time.Unix(p.Used, 0).In(loc).Format("2006-01-02")
		}
		part.Rows = append(part.Rows, row)
	}
	return part, nil
}

// passkeyID reads the passkey a request names.
func (h *Handler) passkeyID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.fail(w, r, http.StatusNotFound)
		return 0, false
	}
	return id, true
}

// renamePasskey gives a passkey of the user another name.
func (h *Handler) renamePasskey(w http.ResponseWriter, r *http.Request) {
	id, ok := h.passkeyID(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	name, valid := passkeyName(r.PostForm.Get("name"))
	if !valid {
		h.profileProblem(w, r, http.StatusBadRequest, "settings.problem.passkey-name")
		return
	}
	switch err := h.db.RenamePasskey(r.Context(), state(r).who.user.ID, id, name); {
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.saved", 0)
		http.Redirect(w, r, h.url("/settings/profile"), http.StatusSeeOther)
	}
}

// deletePasskey removes a passkey of the user. Without logins by password
// the last passkey is the user's only way in, unless a provider is there to
// sign in through: it stays.
func (h *Handler) deletePasskey(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	id, ok := h.passkeyID(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	passkeys, err := h.db.Passkeys(ctx, s.who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if s.system.PasswordLoginOff && len(passkeys) <= 1 && !h.oidcOn(s.system) {
		h.profileProblem(w, r, http.StatusBadRequest, "settings.problem.passkey-last")
		return
	}
	switch err := h.db.DeletePasskey(ctx, s.who.user.ID, id); {
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.passkey-removed", 0)
		http.Redirect(w, r, h.url("/settings/profile"), http.StatusSeeOther)
	}
}

// removeUserPasskeys removes a passkey of another user, or all of them
// (`id` is `all`), on the word of an administrator, for a user who lost the
// device. Whoever may have it also has a login made with it, so the logins
// of the user end. An administrator's own passkeys are removed in their
// profile, where the last one is kept while there is no other way in.
func (h *Handler) removeUserPasskeys(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	u, ok := h.namedUser(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	if u.ID == s.who.user.ID {
		h.showUser(w, r, http.StatusBadRequest, u, "admin.problem.passkey-self")
		return
	}
	var err error
	notice := "notice.passkey-removed"
	if id := r.PostForm.Get("id"); id == "all" {
		err, notice = h.db.DeleteUserPasskeys(ctx, u.ID), "notice.passkeys-removed"
	} else if n, parseErr := strconv.ParseInt(id, 10, 64); parseErr == nil && n > 0 {
		err = h.db.DeletePasskey(ctx, u.ID, n)
	} else {
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	if err == nil {
		err = h.db.DeleteUserSessions(ctx, u.ID, "")
		h.loginsEnded(u.ID)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, notice, 0)
		http.Redirect(w, r, h.url("/admin/users/"+u.Name), http.StatusSeeOther)
	}
}
