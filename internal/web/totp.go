package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/authguard"
	"github.com/juev/freshgo/internal/store"
)

const (
	// totpPeriod is how long a code of the app lasts; a code of the period
	// before or after is taken too, for clocks that are not quite right.
	totpPeriod = 30 * time.Second
	// codeLife is how long a visitor who typed the password has to type
	// the code; codeTries how many wrong codes they may type meanwhile.
	codeLife  = 5 * time.Minute
	codeTries = 5
	// knownCodeLogins bounds the logins that wait for a code.
	knownCodeLogins = 10000
)

var totpOptions = totp.ValidateOpts{Period: uint(totpPeriod / time.Second), Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// totpStep returns the period of the code if it is one of the secret at
// now, give or take a period.
func totpStep(secret, code string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	for _, skew := range []time.Duration{0, -totpPeriod, totpPeriod} {
		at := now.Add(skew)
		want, err := totp.GenerateCodeCustom(secret, at, totpOptions)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return at.Unix() / int64(totpPeriod/time.Second), true
		}
	}
	return 0, false
}

// useTOTP takes the code of the user if it is one of their secret that no
// sign-in has taken yet: a code works once.
func (h *Handler) useTOTP(ctx context.Context, userID int64, code string) (bool, error) {
	taken := false
	err := h.db.UpdateUserSettings(ctx, userID, func(settings map[string]json.RawMessage) error {
		secret := attrs(settings).text("totp_secret")
		last, _ := attrs(settings).number("totp_last")
		step, ok := totpStep(secret, code, h.now())
		if secret == "" || !ok || step <= int64(last) {
			return nil
		}
		attrs(settings).set("totp_last", step)
		taken = true
		return nil
	})
	return taken, err
}

// codeLogin is a login whose password was right and whose code is still
// to be typed.
type codeLogin struct {
	userID     int64
	name, next string
	persistent bool
	expires    time.Time
	tries      int
}

// codeLogins keeps the logins that wait for a code, by the hash of the
// secret the page of the code carries.
type codeLogins struct {
	mu     sync.Mutex
	logins map[string]*codeLogin
}

// add keeps a login until its code is typed, and returns the secret that
// finds it again.
func (c *codeLogins) add(login *codeLogin, now time.Time) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logins == nil {
		c.logins = map[string]*codeLogin{}
	}
	if len(c.logins) >= knownCodeLogins {
		for key, old := range c.logins {
			if !now.Before(old.expires) {
				delete(c.logins, key)
			}
		}
		if len(c.logins) >= knownCodeLogins {
			clear(c.logins)
		}
	}
	login.expires = now.Add(codeLife)
	c.logins[tokenHash(token)] = login
	return token, nil
}

// get returns a copy of the login the secret finds, unless it has run out.
func (c *codeLogins) get(token string, now time.Time) (codeLogin, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	login := c.logins[tokenHash(token)]
	if login == nil || !now.Before(login.expires) {
		delete(c.logins, tokenHash(token))
		return codeLogin{}, false
	}
	return *login, true
}

// failed counts a wrong code, and forgets the login after the last one.
func (c *codeLogins) failed(token string) (left int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	login := c.logins[tokenHash(token)]
	if login == nil {
		return 0
	}
	login.tries++
	if login.tries >= codeTries {
		delete(c.logins, tokenHash(token))
		return 0
	}
	return codeTries - login.tries
}

// done forgets a login whose code was typed.
func (c *codeLogins) done(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.logins, tokenHash(token))
}

// codePage is what the page that asks for the code shows.
type codePage struct {
	Token, Error string
}

func (h *Handler) showCode(w http.ResponseWriter, r *http.Request, status int, page codePage) {
	v := h.view(r, "login", "login.code.heading")
	v.Data = page
	h.render(w, r, status, "code", v)
}

// askCode keeps a login whose password was right until the code of the app
// is typed, and asks for it.
func (h *Handler) askCode(w http.ResponseWriter, r *http.Request, user *store.User, persistent bool, next string) {
	token, err := h.codeLogins.add(&codeLogin{userID: user.ID, name: user.Name, next: next, persistent: persistent}, h.now())
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.showCode(w, r, http.StatusOK, codePage{Token: token})
}

// loginCode logs in a visitor who typed the password and now the code.
// Wrong codes count like wrong passwords, and a login that waits for one
// is forgotten after a few or after a while.
func (h *Handler) loginCode(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	if s.system.AuthType != store.AuthForm {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
	token, code := r.PostFormValue("token"), r.PostFormValue("code")
	texts := h.view(r, "", "login.heading")
	now := h.now()
	login, ok := h.codeLogins.get(token, now)
	if !ok {
		h.showLogin(w, r, http.StatusUnauthorized, loginForm{Error: texts.T("login.code.expired")})
		return
	}
	key := authguard.Key(login.name, authguard.ClientAddr(r, h.proxies))
	if wait := h.guard.Attempt(key, now); wait > 0 {
		w.Header().Set("Retry-After", authguard.RetryAfter(wait))
		h.showCode(w, r, http.StatusTooManyRequests, codePage{Token: token, Error: texts.N("login.locked", int((wait+time.Minute-1)/time.Minute))})
		return
	}
	user, err := h.db.UserByID(r.Context(), login.userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.broken(w, r, err)
		return
	}
	if user == nil || !readPreferences(user).enabled() {
		h.codeLogins.done(token)
		h.showLogin(w, r, http.StatusUnauthorized, loginForm{Error: texts.T("login.failed")})
		return
	}
	taken, err := h.useTOTP(r.Context(), user.ID, code)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if !taken {
		if h.codeLogins.failed(token) == 0 {
			h.showLogin(w, r, http.StatusUnauthorized, loginForm{Name: login.name, Next: login.next, Error: texts.T("login.code.expired")})
			return
		}
		h.showCode(w, r, http.StatusUnauthorized, codePage{Token: token, Error: texts.T("login.code.wrong")})
		return
	}
	h.codeLogins.done(token)
	h.guard.Succeeded(key)
	if err := h.startSession(w, r, user, login.persistent); err != nil {
		h.broken(w, r, err)
		return
	}
	http.Redirect(w, r, h.localTarget(login.next), http.StatusSeeOther)
}

// totpSetup is what the profile shows a user who turns the code on: the
// secret to give the app, as a picture and as text.
type totpSetup struct {
	Secret string
	QR     template.URL
}

// newTOTPSetup returns what turning the code on shows: for the secret the
// form came back with, so that a mistake does not change it, or a new one.
func newTOTPSetup(title, name, secret string) (*totpSetup, error) {
	opts := totp.GenerateOpts{Issuer: title, AccountName: name, Period: uint(totpPeriod / time.Second), Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	if raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret); err == nil && len(raw) == 20 {
		opts.Secret = raw
	}
	key, err := totp.Generate(opts)
	if err != nil {
		return nil, err
	}
	img, err := key.Image(200, 200)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &totpSetup{Secret: key.Secret(), QR: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()))}, nil
}

// changeTOTP turns the code of the app on, with a secret the app has shown
// it knows by a code, or off, with a code of the secret. Either takes the
// password.
func (h *Handler) changeTOTP(w http.ResponseWriter, r *http.Request) {
	ctx, who := r.Context(), state(r).who
	if !h.form(w, r) {
		return
	}
	refuse := func(status int, problem string) { h.profileProblem(w, r, status, problem) }
	if who.prefs.PasswordHash == "" {
		refuse(http.StatusBadRequest, "settings.problem.totp-password")
		return
	}
	// The password and the code are one attempt: a wrong code counts like
	// a wrong password, and only both right clear the count.
	key := authguard.Key(who.user.Name, authguard.ClientAddr(r, h.proxies))
	if wait := h.guard.Attempt(key, h.now()); wait > 0 {
		w.Header().Set("Retry-After", authguard.RetryAfter(wait))
		refuse(http.StatusTooManyRequests, "settings.problem.password-wait")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(who.prefs.PasswordHash), []byte(r.PostForm.Get("password"))) != nil {
		refuse(http.StatusForbidden, "settings.problem.password-wrong")
		return
	}
	code, notice := r.PostForm.Get("code"), ""
	switch r.PostForm.Get("action") {
	case "on":
		secret := r.PostForm.Get("totp_secret")
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
		step, ok := totpStep(secret, code, h.now())
		if err != nil || len(raw) != 20 || !ok {
			refuse(http.StatusBadRequest, "settings.problem.totp-code")
			return
		}
		err = h.db.UpdateUserSettings(ctx, who.user.ID, func(settings map[string]json.RawMessage) error {
			attrs(settings).set("totp_secret", secret)
			attrs(settings).set("totp_last", step)
			return nil
		})
		if err != nil {
			h.broken(w, r, err)
			return
		}
		notice = "notice.totp-on"
	case "off":
		taken, err := h.useTOTP(ctx, who.user.ID, code)
		if err != nil {
			h.broken(w, r, err)
			return
		}
		if !taken {
			refuse(http.StatusBadRequest, "settings.problem.totp-code")
			return
		}
		if err := h.turnOffTOTP(ctx, who.user.ID); err != nil {
			h.broken(w, r, err)
			return
		}
		notice = "notice.totp-off"
	default:
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	h.guard.Succeeded(key)
	h.notify(w, r, notice, 0)
	http.Redirect(w, r, h.url("/settings/profile"), http.StatusSeeOther)
}

// turnOffTOTP lets the user log in by the password alone again.
func (h *Handler) turnOffTOTP(ctx context.Context, userID int64) error {
	return h.db.UpdateUserSettings(ctx, userID, func(settings map[string]json.RawMessage) error {
		delete(settings, "totp_secret")
		delete(settings, "totp_last")
		return nil
	})
}
