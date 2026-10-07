// Package greader serves the Google Reader API the way FreshRSS does, so
// that clients set up for a FreshRSS server keep working against freshgo.
//
// The behaviour follows p/api/greader.php of FreshRSS at commit 219eaf58 and
// is checked against FreshRSS 1.30.1. See docs/specs/greader-api.md.
package greader

import (
	"context"
	"crypto/sha1" // the token format of FreshRSS, which issued the tokens clients hold
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
)

// Alias is the path FreshRSS serves the API at. Requests under it are
// answered like those at the root, so that a client moved over from FreshRSS
// needs no new address.
const Alias = "/api/greader.php"

// maxBody is how much of a request body is read, as in FreshRSS.
const maxBody = 1 << 20

// Options are what a Handler works with.
type Options struct {
	DB        *store.Store
	Refresher *refresh.Refresher
	Hooks     *hooks.Registry
	Log       *slog.Logger
	// BaseURL is the public address of the server, without a trailing
	// slash. When empty, links are built from the request.
	BaseURL string
}

// Handler answers the requests of the Google Reader API.
type Handler struct {
	db        *store.Store
	refresher *refresh.Refresher
	hooks     *hooks.Registry
	log       *slog.Logger
	baseURL   string
	now       func() time.Time
}

// New returns a Handler.
func New(o Options) *Handler {
	return &Handler{db: o.DB, refresher: o.Refresher, hooks: o.Hooks, log: o.Log, baseURL: o.BaseURL, now: time.Now}
}

// request is one API call: what was asked and by whom.
type request struct {
	w http.ResponseWriter
	r *http.Request
	// parts is the path split at slashes, the alias left out; parts[0] is
	// empty.
	parts []string
	query url.Values
	// body is the request body as sent; form is the body read as a form.
	body string
	form url.Values
	user *store.User
	// limits are what the installation lets a user have.
	limits store.Limits
}

// get returns a query parameter, post a form field, either the one found
// first in the query and then in the form. Of a repeated name the last
// value counts, as in PHP.
func (q *request) get(name string) string  { return last(q.query[name]) }
func (q *request) post(name string) string { return last(q.form[name]) }

func (q *request) either(name string) string {
	if v, ok := q.form[name]; ok {
		return last(v)
	}
	return q.get(name)
}

func (q *request) has(name string) bool {
	_, inQuery := q.query[name]
	_, inForm := q.form[name]
	return inQuery || inForm
}

func last(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// posted returns every value of a repeated form field, in order. The body
// is split by hand: a field counts only when its name is written unencoded.
func (q *request) posted(name string) []string {
	var values []string
	prefix := name + "="
	for _, pair := range strings.Split(q.body, "&") {
		if raw, ok := strings.CutPrefix(pair, prefix); ok {
			value, err := url.QueryUnescape(raw)
			if err != nil {
				value = raw
			}
			values = append(values, value)
		}
	}
	return values
}

// number reads a query parameter as an integer; anything else is the
// default.
func (q *request) number(name string, def int64) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(q.get(name)), 10, 64)
	if err != nil {
		return def
	}
	return n
}

// ServeHTTP answers a request under the root of the API or under Alias.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	head := w.Header()
	head.Set("Access-Control-Allow-Headers", "Authorization")
	head.Set("Access-Control-Allow-Methods", "GET, POST")
	head.Set("Access-Control-Allow-Origin", "*")
	head.Set("Access-Control-Max-Age", "600")
	head.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	head.Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, Alias)
	if path == "" && r.URL.RawQuery == "" {
		done(w)
		return
	}
	q := &request{w: w, r: r, parts: strings.Split(path, "/"), query: r.URL.Query()}
	if len(q.parts) < 3 {
		badRequest(w)
		return
	}
	if q.parts[1] == "check" && q.parts[2] == "compatibility" {
		// What FreshRSS tells an administrator about the web server.
		if credentials(r) == "" {
			text(w, http.StatusOK, "FAIL get HTTP Authorization header! Wrong Web server configuration.")
			return
		}
		text(w, http.StatusOK, "PASS")
		return
	}

	ctx := r.Context()
	system, err := h.db.System(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	if !system.APIEnabled {
		// What FreshRSS answers while an administrator has the API off.
		text(w, http.StatusServiceUnavailable, "Service Unavailable!")
		return
	}
	q.limits = system.Limits
	if q.parts[1] != "accounts" {
		user, status, err := h.authenticate(ctx, r)
		switch {
		case err != nil:
			h.fail(w, err)
			return
		case status == http.StatusBadRequest:
			badRequest(w)
			return
		case status != 0:
			unauthorized(w)
			return
		}
		q.user = user
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		badRequest(w)
		return
	}
	q.body = string(raw)
	q.form = url.Values{}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		// What cannot be read as a form is no form, as for PHP.
		if form, err := url.ParseQuery(q.body); err == nil {
			q.form = form
		}
	}

	if q.parts[1] == "accounts" {
		if q.parts[2] == "ClientLogin" && q.has("Email") && q.has("Passwd") {
			h.clientLogin(ctx, q)
			return
		}
		badRequest(w)
		return
	}
	if len(q.parts) < 5 || q.parts[1] != "reader" || q.parts[2] != "api" || q.parts[3] != "0" {
		badRequest(w)
		return
	}
	if q.user == nil {
		unauthorized(w)
		return
	}
	if err := h.dispatch(ctx, q); err != nil {
		if ctx.Err() == nil {
			h.fail(w, err)
		}
	}
}

// errBadRequest is what an endpoint returns for a request it cannot make
// sense of; errUnauthorized for a wrong token.
var (
	errBadRequest   = errors.New("greader: bad request")
	errUnauthorized = errors.New("greader: unauthorized")
)

// dispatch runs the endpoint the path names. An endpoint that answers
// returns nil; one that does not know the request returns errBadRequest.
func (h *Handler) dispatch(ctx context.Context, q *request) error {
	part := func(i int) string {
		if i < len(q.parts) {
			return q.parts[i]
		}
		return ""
	}
	jsonOnly := func(endpoint func(context.Context, *request) error) error {
		if q.get("output") != "json" {
			text(q.w, http.StatusNotImplemented, "Not Implemented!")
			return nil
		}
		return endpoint(ctx, q)
	}
	// Changes are guarded by the token of the "token" endpoint.
	withToken := func(endpoint func(context.Context, *request) error) error {
		token, err := h.token(ctx, q.user)
		if err != nil {
			return err
		}
		if !h.validToken(token, strings.TrimSpace(q.post("T"))) {
			return errUnauthorized
		}
		return endpoint(ctx, q)
	}

	switch part(4) {
	case "stream":
		return h.stream(ctx, q)
	case "tag":
		if part(5) == "list" {
			return jsonOnly(h.tagList)
		}
	case "subscription":
		switch part(5) {
		case "export":
			return h.subscriptionExport(ctx, q)
		case "import":
			if q.r.Method == http.MethodPost && q.body != "" {
				return h.subscriptionImport(ctx, q)
			}
		case "list":
			return jsonOnly(h.subscriptionList)
		case "edit":
			if q.has("s") && q.has("ac") {
				return h.subscriptionEdit(ctx, q)
			}
		case "quickadd":
			if q.has("quickadd") {
				return h.quickAdd(ctx, q)
			}
		}
	case "unread-count":
		return jsonOnly(h.unreadCount)
	case "edit-tag":
		return withToken(h.editTag)
	case "rename-tag":
		return withToken(h.renameTag)
	case "disable-tag":
		return withToken(h.disableTag)
	case "mark-all-as-read":
		return withToken(h.markAllAsRead)
	case "token":
		token, err := h.token(ctx, q.user)
		if err != nil {
			return err
		}
		text(q.w, http.StatusOK, token+"\n")
		return nil
	case "user-info":
		return h.userInfo(q)
	}
	return errBadRequest
}

// fail answers with what an endpoint could not do.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadRequest):
		badRequest(w)
	case errors.Is(err, errUnauthorized):
		unauthorized(w)
	default:
		h.log.Error("Google Reader API request failed", "error", err)
		text(w, http.StatusInternalServerError, "Internal Server Error!")
	}
}

func text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// done is the answer to a change that went through.
func done(w http.ResponseWriter) {
	text(w, http.StatusOK, "OK")
}

func badRequest(w http.ResponseWriter) {
	text(w, http.StatusBadRequest, "Bad Request!")
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Google-Bad-Token", "true")
	text(w, http.StatusUnauthorized, "Unauthorized!")
}

// writeJSON answers with a JSON document followed by a line break.
func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	return encode(w, v)
}

// encode writes v the way FreshRSS writes JSON: nothing but what JSON itself
// requires is escaped.
func encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// credentials returns the value of "GoogleLogin auth=" in the Authorization
// header, read the way PHP reads a query string, or "".
func credentials(r *http.Request) string {
	value := ""
	for _, pair := range strings.Split(r.Header.Get("Authorization"), "&") {
		name, raw, _ := strings.Cut(pair, "=")
		name, err := url.QueryUnescape(name)
		if err != nil {
			continue
		}
		// PHP turns the spaces and dots of a variable name into underscores.
		name = strings.NewReplacer(" ", "_", ".", "_").Replace(strings.TrimLeft(name, " "))
		if name != "GoogleLogin_auth" {
			continue
		}
		if value, err = url.QueryUnescape(raw); err != nil {
			value = raw
		}
	}
	return value
}

// authenticate finds the user the Authorization header names. A request
// without the header has no user and no status: whether that is an error is
// up to the endpoint. status is the HTTP status to refuse the request with.
func (h *Handler) authenticate(ctx context.Context, r *http.Request) (user *store.User, status int, err error) {
	name, token, ok := strings.Cut(credentials(r), "/")
	if !ok {
		return nil, 0, nil
	}
	if !store.ValidUserName(name) {
		return nil, http.StatusBadRequest, nil
	}
	user, err = h.db.UserByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, http.StatusUnauthorized, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !readSettings(user.Settings).enabled {
		return nil, http.StatusUnauthorized, nil
	}
	want, err := h.authToken(ctx, user)
	if err != nil {
		return nil, 0, err
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		h.log.Warn("invalid API authorisation", "user", name)
		return nil, http.StatusUnauthorized, nil
	}
	return user, 0, nil
}

// authToken is the secret part of what ClientLogin hands out: the same
// value FreshRSS computes, so that a token issued there stays valid after
// the installation has been imported.
func (h *Handler) authToken(ctx context.Context, u *store.User) (string, error) {
	salt, err := h.db.Salt(ctx)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(salt + u.Name + u.APIPasswordHash))
	return hex.EncodeToString(sum[:]), nil
}

// token is what the "token" endpoint hands out for changes: the secret of
// authToken padded to the 57 characters clients expect.
func (h *Handler) token(ctx context.Context, u *store.User) (string, error) {
	secret, err := h.authToken(ctx, u)
	if err != nil {
		return "", err
	}
	return secret + strings.Repeat("Z", 57-len(secret)), nil
}

// validToken reports whether a change may go ahead with the token sent.
// FeedMe sends none and Reeder sends "x"; FreshRSS lets both through, the
// Authorization header having been checked already.
func (h *Handler) validToken(want, sent string) bool {
	if sent == "" || sent == "x" {
		return true
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(sent)) == 1 {
		return true
	}
	h.log.Warn("invalid POST token")
	return false
}

func (h *Handler) clientLogin(ctx context.Context, q *request) {
	name, password := q.either("Email"), q.either("Passwd")
	if !store.ValidUserName(name) {
		badRequest(q.w)
		return
	}
	user, err := h.db.UserByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		unauthorized(q.w)
		return
	}
	if err != nil {
		h.fail(q.w, err)
		return
	}
	if user.APIPasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.APIPasswordHash), []byte(password)) != nil {
		h.log.Warn("password API mismatch", "user", name)
		unauthorized(q.w)
		return
	}
	secret, err := h.authToken(ctx, user)
	if err != nil {
		h.fail(q.w, err)
		return
	}
	auth := name + "/" + secret
	// LSID is for Vienna RSS.
	text(q.w, http.StatusOK, "SID="+auth+"\nLSID=null\nAuth="+auth+"\n")
}

func (h *Handler) userInfo(q *request) error {
	type info struct {
		UserID        string `json:"userId"`
		UserName      string `json:"userName"`
		UserProfileID string `json:"userProfileId"`
		UserEmail     string `json:"userEmail"`
	}
	name := q.user.Name
	return writeJSON(q.w, info{name, name, name, readSettings(q.user.Settings).mail})
}

// settings are the keys of a user's settings the API depends on, under the
// names FreshRSS gives them in the user's config.php.
type settings struct {
	enabled  bool
	mail     string
	language string
}

func readSettings(raw json.RawMessage) settings {
	var s struct {
		Enabled  *bool  `json:"enabled"`
		Mail     string `json:"mail_login"`
		Language string `json:"language"`
	}
	// Settings of another shape are the defaults.
	_ = json.Unmarshal(raw, &s)
	return settings{enabled: s.Enabled == nil || *s.Enabled, mail: s.Mail, language: s.Language}
}
