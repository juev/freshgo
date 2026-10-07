package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/store"
)

// Mailer sends the letters the interface writes.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Pages a user whose address is not confirmed yet may still ask for.
var unconfirmedPaths = []string{"/validate-email", "/settings/profile", "/tos"}

// unconfirmed reports whether the user has yet to confirm their e-mail
// address before using the interface. Administrators are not held up: one of
// them has to be able to switch the rule off.
func unconfirmed(s *request) bool {
	return s.system.ForceEmailValidation && s.who != nil && !s.who.anonymous && !s.who.admin && s.who.prefs.EmailValidationToken != ""
}

// registrationOpen reports whether visitors can make an account for
// themselves, and when they cannot, the key of the text that says why.
func (h *Handler) registrationOpen(ctx context.Context, system store.System) (bool, string, error) {
	if system.AuthType != store.AuthForm {
		return false, "register.closed", nil
	}
	if system.ForceEmailValidation && h.mailer == nil {
		return false, "register.no-mail", nil
	}
	if limit := system.Limits.MaxRegistrations; limit > 0 {
		users, err := h.db.Users(ctx)
		if err != nil {
			return false, "", err
		}
		if len(users) >= limit {
			return false, "register.closed", nil
		}
	}
	return true, "", nil
}

// registerPage is what the page of registration shows.
type registerPage struct {
	Name, Email string
	// Closed is what a visitor reads when nobody can register.
	Closed string
	// NeedsEmail says the address is asked for; HasTerms that there are
	// terms to accept.
	NeedsEmail, HasTerms bool
	Problem              string
}

func (h *Handler) showRegister(w http.ResponseWriter, r *http.Request, status int, page registerPage, problem string) {
	s := state(r)
	v := h.view(r, "login", "register.heading")
	open, why, err := h.registrationOpen(r.Context(), s.system)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if !open {
		status = http.StatusForbidden
		page.Closed = v.T(why)
		if why == "register.closed" && s.system.ClosedRegistrationMessage != "" {
			page.Closed = s.system.ClosedRegistrationMessage
		}
	}
	page.NeedsEmail, page.HasTerms = s.system.ForceEmailValidation, strings.TrimSpace(s.system.TOS) != ""
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "register", v)
}

func (h *Handler) registerPage(w http.ResponseWriter, r *http.Request) {
	if s := state(r); s.who != nil && !s.who.anonymous {
		http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
		return
	}
	h.showRegister(w, r, http.StatusOK, registerPage{}, "")
}

var errRegistrationClosed = errors.New("web: nobody can register")

// register makes an account for the visitor and logs them in.
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	// A registration form is as small as a login form.
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
	if err := r.ParseForm(); err != nil || !isText(r.PostForm) {
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	form := r.PostForm
	name, email := strings.TrimSpace(form.Get("username")), strings.TrimSpace(form.Get("email"))
	page := registerPage{Name: name, Email: email}
	if open, _, err := h.registrationOpen(ctx, s.system); err != nil {
		h.broken(w, r, err)
		return
	} else if !open {
		h.showRegister(w, r, http.StatusForbidden, page, "")
		return
	}
	problem := passwordProblem(form.Get("password"), form.Get("again"))
	switch {
	case !store.ValidUserName(name):
		problem = "admin.problem.name"
	case problem != "":
	case email != "" && (!emailAddress.MatchString(email) || len(email) > 191), email == "" && s.system.ForceEmailValidation:
		problem = "settings.problem.email"
	case strings.TrimSpace(s.system.TOS) != "" && form.Get("accept_tos") == "":
		problem = "register.problem.terms"
	}
	if problem != "" {
		h.showRegister(w, r, http.StatusBadRequest, page, problem)
		return
	}
	hash, err := hashPassword(form.Get("password"))
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "", "register.heading")
	settings := attrs{}
	settings.set("passwordHash", hash)
	settings.set("language", v.Lang())
	settings.set("mail_login", email)
	token := ""
	if s.system.ForceEmailValidation {
		if token, err = newQueryToken(); err != nil {
			h.broken(w, r, err)
			return
		}
		settings.set("email_validation_token", token)
	}
	user := &store.User{Name: name, Settings: settings.raw()}
	err = h.db.InTx(ctx, func(tx *store.Store) error {
		// Counted again where no other registration can slip in between.
		if limit := s.system.Limits.MaxRegistrations; limit > 0 {
			users, err := tx.Users(ctx)
			if err != nil {
				return err
			}
			if len(users) >= limit {
				return errRegistrationClosed
			}
		}
		return tx.CreateUser(ctx, user)
	})
	switch {
	case errors.Is(err, errRegistrationClosed):
		h.showRegister(w, r, http.StatusForbidden, page, "")
		return
	case errors.Is(err, store.ErrConflict):
		h.showRegister(w, r, http.StatusBadRequest, page, "admin.problem.name-taken")
		return
	case err != nil:
		h.broken(w, r, err)
		return
	}
	h.log.Info("user registered", "name", name)
	if token != "" {
		h.sendValidation(r, v, user.Name, email, token)
	}
	if err := h.startSession(w, r, user, false); err != nil {
		h.broken(w, r, err)
		return
	}
	http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
}

// sendValidation writes the user the letter with the link that confirms
// their address. A letter that cannot be sent is logged: the page that asks
// for the confirmation sends it again.
func (h *Handler) sendValidation(r *http.Request, v *view, name, email, token string) {
	if h.mailer == nil || email == "" {
		return
	}
	link := h.absolute(r, "/validate-email") + "?" + url.Values{"user": {name}, "token": {token}}.Encode()
	site := state(r).system.Title
	if err := h.mailer.Send(r.Context(), email, v.T("validate.mail.subject", site), v.T("validate.mail.body", name, site, link)); err != nil {
		h.log.Warn("letter was not sent", "user", name, "error", err)
	}
}

// absolute is the address of a page with the scheme and the host a letter
// needs: the public address of the server, or else what the request says.
func (h *Handler) absolute(r *http.Request, path string) string {
	if h.baseURL != "" {
		return strings.TrimSuffix(h.baseURL, "/") + path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + h.url(path)
}

// validatePage is what the page that waits for a confirmation shows.
type validatePage struct {
	Email   string
	CanSend bool
}

// validateEmail confirms the address of the user the link names, or shows
// a user who has yet to confirm theirs where they stand.
func (h *Handler) validateEmail(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	params := r.URL.Query()
	if name, token := params.Get("user"), params.Get("token"); token != "" {
		var user *store.User
		if store.ValidUserName(name) {
			var err error
			if user, err = h.userNamed(ctx, name); err != nil {
				h.broken(w, r, err)
				return
			}
		}
		if user == nil || subtle.ConstantTimeCompare([]byte(readPreferences(user).EmailValidationToken), []byte(token)) != 1 {
			h.fail(w, r, http.StatusNotFound)
			return
		}
		err := h.db.UpdateUserSettings(ctx, user.ID, func(settings map[string]json.RawMessage) error {
			attrs(settings).set("email_validation_token", "")
			return nil
		})
		if err != nil {
			h.broken(w, r, err)
			return
		}
		h.notify(w, r, "notice.email-confirmed", 0)
		http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
		return
	}
	if !unconfirmed(s) {
		http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
		return
	}
	v := h.view(r, "login", "validate.heading")
	v.Data = validatePage{Email: readAttrs(s.who.user.Settings).text("mail_login"), CanSend: h.mailer != nil}
	h.render(w, r, http.StatusOK, "validate", v)
}

// resendValidation sends the letter with the link once more.
func (h *Handler) resendValidation(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	if !h.form(w, r) {
		return
	}
	if unconfirmed(s) {
		v := h.view(r, "", "validate.heading")
		h.sendValidation(r, v, s.who.user.Name, readAttrs(s.who.user.Settings).text("mail_login"), s.who.prefs.EmailValidationToken)
		h.notify(w, r, "notice.email-sent", 0)
	}
	http.Redirect(w, r, h.url("/validate-email"), http.StatusSeeOther)
}

// termsPage shows the terms of the installation.
func (h *Handler) termsPage(w http.ResponseWriter, r *http.Request) {
	terms := strings.TrimSpace(state(r).system.TOS)
	if terms == "" {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	v := h.view(r, "", "tos.heading")
	// What an administrator wrote is cleaned like the text of an entry: a
	// page of terms needs no script.
	v.Data = template.HTML(sanitize.HTML(terms, "", nil)) //nolint:gosec // cleaned on this line
	h.render(w, r, http.StatusOK, "tos", v)
}
