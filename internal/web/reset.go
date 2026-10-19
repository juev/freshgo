package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/store"
)

// resetLife is how long the link of a letter that resets a password works.
const resetLife = time.Hour

// resetOn reports whether users can have their password reset by a letter:
// they log in by password and the server can send letters.
func (h *Handler) resetOn(system store.System) bool {
	return system.AuthType == store.AuthForm && h.mailer != nil
}

// resetPage is what the page that resets a password shows: the form that
// asks for a letter, what became of it, or, with the link of the letter, the
// form of the new password.
type resetPage struct {
	Name, Token string
	Sent        bool
	Problem     string
}

func (h *Handler) showReset(w http.ResponseWriter, r *http.Request, status int, page resetPage) {
	v := h.view(r, "login", "reset.heading")
	if page.Problem != "" {
		page.Problem = v.T(page.Problem)
	}
	v.Data = page
	h.render(w, r, status, "reset", v)
}

// resetUser returns the user the link of a letter names, if its token is
// the one of a reset that has not run out; nil otherwise.
func (h *Handler) resetUser(ctx context.Context, name, token string) (*store.User, error) {
	if !store.ValidUserName(name) || token == "" {
		return nil, nil
	}
	user, err := h.userNamed(ctx, name)
	if err != nil || user == nil {
		return nil, err
	}
	settings := readAttrs(user.Settings)
	expires, _ := settings.number("password_reset_expires")
	if subtle.ConstantTimeCompare([]byte(settings.text("password_reset")), []byte(tokenHash(token))) != 1 ||
		!h.now().Before(time.Unix(int64(expires), 0)) || !readPreferences(user).enabled() {
		return nil, nil
	}
	return user, nil
}

// resetPassword shows the form that asks for a letter, or the form of the
// new password to the link of one.
func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	if !h.resetOn(state(r).system) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	params := r.URL.Query()
	name, token := params.Get("user"), params.Get("token")
	if token == "" {
		h.showReset(w, r, http.StatusOK, resetPage{})
		return
	}
	user, err := h.resetUser(r.Context(), name, token)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if user == nil {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	h.showReset(w, r, http.StatusOK, resetPage{Name: user.Name, Token: token})
}

// sendReset writes the user the form names a letter with a link to choose
// a new password. The page says the same whether or not there is such a
// user with an address: which names exist is nobody's business.
func (h *Handler) sendReset(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	if !h.resetOn(s.system) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
	if err := r.ParseForm(); err != nil || !isText(r.PostForm) {
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("username"))
	var user *store.User
	if store.ValidUserName(name) {
		var err error
		if user, err = h.userNamed(ctx, name); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	// Only an address the user has confirmed is one a letter may go to.
	email := ""
	if user != nil && readPreferences(user).enabled() && readPreferences(user).EmailValidationToken == "" {
		email = readAttrs(user.Settings).text("mail_login")
	}
	if email != "" && h.letters.Attempt(user.Name, h.now()) == 0 {
		token, err := newToken()
		if err != nil {
			h.broken(w, r, err)
			return
		}
		err = h.db.UpdateUserSettings(ctx, user.ID, func(settings map[string]json.RawMessage) error {
			attrs(settings).set("password_reset", tokenHash(token))
			attrs(settings).set("password_reset_expires", h.now().Add(resetLife).Unix())
			return nil
		})
		if err != nil {
			h.broken(w, r, err)
			return
		}
		v := h.view(r, "", "reset.heading")
		link := h.absolute(r, "/reset") + "?" + url.Values{"user": {user.Name}, "token": {token}}.Encode()
		site := s.system.Title
		if err := h.mailer.Send(ctx, email, v.T("reset.mail.subject", site), v.T("reset.mail.body", user.Name, site, link)); err != nil {
			h.log.Warn("letter was not sent", "user", user.Name, "error", err)
		}
	}
	h.showReset(w, r, http.StatusOK, resetPage{Sent: true})
}

// saveReset gives the user of the link a new password, once: the link
// stops working, and every login of the user ends.
func (h *Handler) saveReset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.resetOn(state(r).system) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
	if err := r.ParseForm(); err != nil || !isText(r.PostForm) {
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	form := r.PostForm
	user, err := h.resetUser(ctx, form.Get("user"), form.Get("token"))
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if user == nil {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	password := form.Get("new")
	if problem := passwordProblem(password, form.Get("again")); problem != "" {
		h.showReset(w, r, http.StatusBadRequest, resetPage{Name: user.Name, Token: form.Get("token"), Problem: problem})
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	token := tokenHash(form.Get("token"))
	used := false
	err = h.db.InTx(ctx, func(tx *store.Store) error {
		err := tx.UpdateUserSettings(ctx, user.ID, func(settings map[string]json.RawMessage) error {
			// A link that was used meanwhile is used up.
			if subtle.ConstantTimeCompare([]byte(attrs(settings).text("password_reset")), []byte(token)) != 1 {
				used = true
				return nil
			}
			attrs(settings).set("passwordHash", hash)
			delete(settings, "password_reset")
			delete(settings, "password_reset_expires")
			return nil
		})
		if err != nil || used {
			return err
		}
		return tx.DeleteUserSessions(ctx, user.ID, "")
	})
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if used {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	h.loginsEnded(user.ID)
	h.log.Info("password reset by letter", "user", user.Name)
	h.notify(w, r, "notice.password-reset", 0)
	http.Redirect(w, r, h.url("/login"), http.StatusSeeOther)
}
