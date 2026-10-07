package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/store"
)

// sessionCookie holds the secret of a login.
const sessionCookie = "freshgo_session"

const (
	// sessionIdle is how long a login that is not to be remembered lasts
	// without being used; the cookie itself goes with the browser.
	sessionIdle = 24 * time.Hour
	// sessionTouch is how stale the record of the last use may get: the
	// database is not written to on every request.
	sessionTouch = time.Hour
)

// maxLoginForm bounds the body of a login request, in bytes.
const maxLoginForm = 16 << 10

// Headers a reverse proxy names the user in, when users are told apart by it.
var proxyUserHeaders = []string{"Remote-User", "X-WebAuth-User"}

// preferences are the settings of a user the frame of every page depends
// on, under the names FreshRSS gives them.
type preferences struct {
	Language string `json:"language"`
	// DarkMode is "auto" or "no" in FreshRSS; freshgo adds "dark".
	DarkMode     string `json:"darkMode"`
	ContentWidth string `json:"content_width"`
	// Enabled is false for a user who is kept out.
	Enabled *bool `json:"enabled"`
	IsAdmin bool  `json:"is_admin"`
	// Token opens the entries of the user as a feed to whoever has it.
	Token string `json:"token"`
	// PasswordHash is the bcrypt hash of the password of the web interface.
	PasswordHash string `json:"passwordHash"`
}

func readPreferences(u *store.User) preferences {
	var p preferences
	// Settings that cannot be read are settings nobody made.
	_ = json.Unmarshal(u.Settings, &p)
	return p
}

func (p preferences) enabled() bool { return p.Enabled == nil || *p.Enabled }

// theme is the value of data-theme the stylesheet understands.
func (p preferences) theme() string {
	switch p.DarkMode {
	case "no":
		return "light"
	case "dark":
		return "dark"
	default:
		return "auto"
	}
}

// identity is who a request is made as.
type identity struct {
	user  *store.User
	prefs preferences
	// session is the login the request came with, nil when the user is
	// known in another way.
	session *store.Session
	// anonymous is a visitor who has not logged in and reads what the
	// default user reads, without being able to change anything.
	anonymous bool
	admin     bool
}

// request is what the interface knows about a request before any page looks
// at it.
type request struct {
	system store.System
	// who is nil for a visitor nobody vouches for.
	who *identity
}

type requestKey struct{}

// state returns what ServeHTTP found out about the request.
func state(r *http.Request) *request {
	if s, ok := r.Context().Value(requestKey{}).(*request); ok {
		return s
	}
	return &request{system: store.DefaultSystem()}
}

// identify finds out who makes the request.
func (h *Handler) identify(r *http.Request, system store.System) (*identity, error) {
	ctx := r.Context()
	var (
		user    *store.User
		session *store.Session
		err     error
	)
	switch system.AuthType {
	case store.AuthNone:
		user, err = h.userNamed(ctx, system.DefaultUser)
	case store.AuthHTTP:
		user, err = h.proxyUser(r, system)
	default:
		user, session, err = h.sessionUser(r, system)
	}
	if err != nil {
		return nil, err
	}
	if user != nil {
		prefs := readPreferences(user)
		if prefs.enabled() {
			return &identity{
				user: user, prefs: prefs, session: session,
				admin: prefs.IsAdmin || user.Name == system.DefaultUser,
			}, nil
		}
	}
	if !system.AllowAnonymous {
		return nil, nil
	}
	guestOf, err := h.userNamed(ctx, system.DefaultUser)
	if err != nil || guestOf == nil {
		return nil, err
	}
	prefs := readPreferences(guestOf)
	if !prefs.enabled() {
		return nil, nil
	}
	return &identity{user: guestOf, prefs: prefs, anonymous: true}, nil
}

// userNamed returns the user with the name, nil when there is none.
func (h *Handler) userNamed(ctx context.Context, name string) (*store.User, error) {
	if name == "" {
		return nil, nil
	}
	u, err := h.db.UserByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

// sessionUser returns the user the cookie of the request logs in, and the
// login itself; both nil without a valid one.
func (h *Handler) sessionUser(r *http.Request, system store.System) (*store.User, *store.Session, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, nil, nil
	}
	ctx, now := r.Context(), h.now()
	session, err := h.db.Session(ctx, tokenHash(cookie.Value), now.Unix())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	user, err := h.db.UserByID(ctx, session.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if now.Sub(time.Unix(session.Used, 0)) >= sessionTouch {
		session.Used, session.Expires = now.Unix(), now.Add(sessionLife(session.Persistent, system)).Unix()
		if err := h.db.TouchSession(ctx, session.TokenHash, session.Used, session.Expires); err != nil {
			return nil, nil, err
		}
	}
	return user, session, nil
}

// sessionLife is how long a login lasts after its last use.
func sessionLife(persistent bool, system store.System) time.Duration {
	if persistent && system.Limits.CookieDuration > 0 {
		return time.Duration(system.Limits.CookieDuration) * time.Second
	}
	return sessionIdle
}

// proxyUser returns the user a trusted reverse proxy says the request comes
// from, created on the spot if the installation allows it; nil when the
// proxy names nobody, is not trusted, or names a user there is not.
func (h *Handler) proxyUser(r *http.Request, system store.System) (*store.User, error) {
	if !h.fromProxy(r) {
		return nil, nil
	}
	name := ""
	for _, header := range proxyUserHeaders {
		for _, value := range r.Header.Values(header) {
			switch {
			case value == "" || value == name:
			case name == "":
				name = value
			default:
				// Two names are no name.
				return nil, nil
			}
		}
	}
	if !store.ValidUserName(name) {
		return nil, nil
	}
	ctx := r.Context()
	user, err := h.userNamed(ctx, name)
	if err != nil || user != nil || !system.HTTPAuthAutoRegister {
		return user, err
	}
	user = &store.User{Name: name}
	switch err := h.db.CreateUser(ctx, user); {
	case errors.Is(err, store.ErrConflict):
		// Another request of the same user was quicker.
		return h.userNamed(ctx, name)
	case err != nil:
		return nil, err
	}
	h.log.Info("user created on the word of the reverse proxy", "user", name)
	return user, nil
}

// fromProxy reports whether the request was made by a reverse proxy whose
// word is taken.
func (h *Handler) fromProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, proxy := range h.proxies {
		if proxy.Contains(addr) {
			return true
		}
	}
	return false
}

// newToken returns the secret of a new login.
func newToken() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

// tokenHash is what the database knows a login by.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// secure reports whether the browser reaches the server over HTTPS.
func (h *Handler) secure(r *http.Request) bool {
	return r.TLS != nil || strings.HasPrefix(h.baseURL, "https://")
}

// setSession hands the browser the secret of a login, or takes it back when
// token is empty.
func (h *Handler) setSession(w http.ResponseWriter, r *http.Request, token string, life time.Duration) {
	cookie := &http.Cookie{
		Name: sessionCookie, Value: token, Path: h.prefix + "/",
		HttpOnly: true, Secure: h.secure(r), SameSite: http.SameSiteLaxMode,
	}
	switch {
	case token == "":
		cookie.MaxAge = -1
	case life > 0:
		cookie.MaxAge = int(life / time.Second)
	}
	http.SetCookie(w, cookie)
}

// decoys are hashes no password matches, by bcrypt cost: comparing against
// one takes as long as comparing against the hash of a user.
type decoys struct {
	mu     sync.Mutex
	byCost map[int][]byte
}

func (d *decoys) withCost(cost int) []byte {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if hash, ok := d.byCost[cost]; ok {
		return hash
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("nobody has this password"), cost)
	if err != nil {
		// Nothing compares equal to no hash at all, only faster.
		return nil
	}
	if d.byCost == nil {
		d.byCost = map[int][]byte{}
	}
	d.byCost[cost] = hash
	return hash
}

// checkPassword returns the user with the name if the password is theirs
// and they may log in, nil otherwise.
//
// Refusing must take as long whatever the reason, or the time tells which
// names exist. A name without a usable hash is therefore checked against a
// decoy as costly as the hash of the default user: that of the
// installation's own users, whether they were imported from FreshRSS, which
// hashes cheaper, or created here.
func (h *Handler) checkPassword(ctx context.Context, system store.System, name, password string) (*store.User, error) {
	var user *store.User
	if store.ValidUserName(name) {
		var err error
		if user, err = h.userNamed(ctx, name); err != nil {
			return nil, err
		}
	}
	if user != nil {
		if prefs := readPreferences(user); prefs.enabled() && prefs.PasswordHash != "" {
			if bcrypt.CompareHashAndPassword([]byte(prefs.PasswordHash), []byte(password)) != nil {
				return nil, nil
			}
			return user, nil
		}
	}
	cost := bcrypt.DefaultCost
	if model, err := h.userNamed(ctx, system.DefaultUser); err != nil {
		return nil, err
	} else if model != nil {
		if c, err := bcrypt.Cost([]byte(readPreferences(model).PasswordHash)); err == nil {
			cost = c
		}
	}
	_ = bcrypt.CompareHashAndPassword(h.decoys.withCost(cost), []byte(password))
	return nil, nil
}

// Failed logins that are let through before a pause is imposed, and the
// pauses: the first, then twice as long with every further failure, up to
// the longest.
const (
	freeFailures = 5
	firstPause   = 30 * time.Second
	longestPause = 15 * time.Minute
	// knownFailures bounds what the guard remembers.
	knownFailures = 10000
)

// guard slows down guessing of passwords: after a run of failed logins for
// a name from an address, further ones from there have to wait.
type guard struct {
	mu       sync.Mutex
	failures map[string]*failure
}

type failure struct {
	count int
	// until is when the next attempt is heard; last when the last failed.
	until, last time.Time
}

// attempt asks whether a login for the name from the address may be tried
// now. It returns how long the attempt has to wait, or zero after counting
// it as failed: the count is taken before the password is looked at, so
// that attempts made at once cannot all slip through, and is dropped by
// succeeded.
func (g *guard) attempt(key string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failures == nil {
		g.failures = map[string]*failure{}
	}
	f := g.failures[key]
	if f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	if f == nil && len(g.failures) >= knownFailures {
		// Forget what no longer holds anybody back, and everything if that
		// is not enough: memory is not what an attacker gets to fill.
		for k, old := range g.failures {
			if now.Sub(old.last) > longestPause {
				delete(g.failures, k)
			}
		}
		if len(g.failures) >= knownFailures {
			clear(g.failures)
		}
	}
	if f == nil || now.Sub(f.last) > longestPause {
		f = &failure{}
		g.failures[key] = f
	}
	f.count++
	f.last = now
	if f.count >= freeFailures {
		pause := firstPause << min(f.count-freeFailures, 10)
		f.until = now.Add(min(pause, longestPause))
	}
	return 0
}

// succeeded forgets the attempts before a login that worked, that one
// included.
func (g *guard) succeeded(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, key)
}

// guardKey tells attempts apart by name and by where they come from. The
// name is one a user can have, so the key is short.
func (h *Handler) guardKey(r *http.Request, name string) string {
	return strings.ToLower(name) + "\x00" + h.clientAddr(r)
}

// clientAddr returns the address a request comes from: behind a trusted
// reverse proxy the one the proxy says it took the request from, which is
// the last it added to X-Forwarded-For.
func (h *Handler) clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !h.fromProxy(r) {
		return host
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return host
	}
	hops := strings.Split(forwarded[len(forwarded)-1], ",")
	if client, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
		return client.Unmap().String()
	}
	return host
}

// localTarget returns where to go after logging in: the page the visitor
// was after, if it is one of this interface, the start page otherwise.
func (h *Handler) localTarget(next string) string {
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return h.url("/")
	}
	// next is spelled as the browser sent it: without the public path.
	return h.url(u.RequestURI())
}

// loginForm is what the login page shows.
type loginForm struct {
	Name, Next string
	// Error is the message of a failed attempt, empty before the first.
	Error string
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	next := r.URL.Query().Get("next")
	if s.system.AuthType != store.AuthForm || (s.who != nil && !s.who.anonymous) {
		http.Redirect(w, r, h.localTarget(next), http.StatusSeeOther)
		return
	}
	v := h.view(r, "login", "login.heading")
	v.Data = loginForm{Next: next}
	h.render(w, r, http.StatusOK, "login", v)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	if s.system.AuthType != store.AuthForm {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	// A login form is small; what is not is not read.
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
	name, password := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password")
	next := r.PostFormValue("next")
	refuse := func(status int, message string) {
		v := h.view(r, "login", "login.heading")
		v.Data = loginForm{Name: name, Next: next, Error: message}
		h.render(w, r, status, "login", v)
	}
	texts := h.view(r, "", "login.heading")

	now := h.now()
	// A name no user can have is refused without a look at anything: it
	// has no password to guess, and nothing of it is kept.
	if !store.ValidUserName(name) {
		refuse(http.StatusUnauthorized, texts.T("login.failed"))
		return
	}
	key := h.guardKey(r, name)
	if wait := h.guard.attempt(key, now); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		refuse(http.StatusTooManyRequests, texts.N("login.locked", int((wait+time.Minute-1)/time.Minute)))
		return
	}
	user, err := h.checkPassword(r.Context(), s.system, name, password)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if user == nil {
		// One message whatever was wrong: which names exist is nobody's
		// business.
		refuse(http.StatusUnauthorized, texts.T("login.failed"))
		return
	}
	h.guard.succeeded(key)

	token, err := newToken()
	if err != nil {
		h.broken(w, r, err)
		return
	}
	persistent := r.PostFormValue("remember") != ""
	life := sessionLife(persistent, s.system)
	session := &store.Session{
		TokenHash: tokenHash(token), UserID: user.ID,
		Created: now.Unix(), Used: now.Unix(), Expires: now.Add(life).Unix(),
		Persistent: persistent, Authenticated: now.Unix(),
	}
	// Logging in is when the logins that have ended are cleared away.
	if err := h.db.DeleteExpiredSessions(r.Context(), now.Unix()); err != nil {
		h.broken(w, r, err)
		return
	}
	if err := h.db.CreateSession(r.Context(), session); err != nil {
		h.broken(w, r, err)
		return
	}
	if !persistent {
		life = 0
	}
	h.setSession(w, r, token, life)
	http.Redirect(w, r, h.localTarget(next), http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if who := state(r).who; who != nil && who.session != nil {
		if err := h.db.DeleteSession(r.Context(), who.session.TokenHash); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	h.setSession(w, r, "", 0)
	http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
}

// Who a page is for.
const (
	// everybody includes visitors nobody vouches for.
	everybody = iota
	// readers are users and, where the installation lets them in,
	// anonymous visitors.
	readers
	// members are users who are known to be who they are.
	members
)

// protect lets a page be asked for only by those it is for. A visitor who
// could log in to get there is sent to the login page when it is a page
// they ask for, and refused otherwise.
func (h *Handler) protect(level int, page http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := state(r)
		allowed := level == everybody ||
			s.who != nil && (level == readers || !s.who.anonymous)
		switch {
		case allowed:
			page(w, r)
		case s.system.AuthType == store.AuthForm && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			target := h.url("/login") + "?next=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusSeeOther)
		default:
			h.fail(w, r, http.StatusForbidden)
		}
	}
}
