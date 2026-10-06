package greader

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

const (
	referenceDir  = "../../testdata/reference"
	casesFile     = referenceDir + "/api/cases.json"
	responsesFile = referenceDir + "/api/responses.json"
	// envRecord is the address of a reference FreshRSS: with it set, the
	// test asks FreshRSS instead of freshgo and writes the answers down.
	// testdata/reference/api/generate.sh does that.
	envRecord = "FRESHGO_RECORD_API"
)

// apiCase is one request of the comparison with FreshRSS. The cases run in
// order against one installation, so a case sees what the ones before it
// have changed.
type apiCase struct {
	Name string `json:"name"`
	// User is who asks; alice when empty.
	User   string `json:"user,omitempty"`
	Method string `json:"method,omitempty"`
	// Path follows /api/greader.php, Query the question mark. Both and Body
	// may hold placeholders, see expand.
	Path  string `json:"path"`
	Query string `json:"query,omitempty"`
	Body  string `json:"body,omitempty"`
	// ContentType of the body; a form when empty.
	ContentType string `json:"contentType,omitempty"`
	// Auth is "none" for a request without the Authorization header, "bad"
	// for a wrong token, or the value of the header after "raw:".
	Auth string `json:"auth,omitempty"`
	// AliasOnly marks a request that has no counterpart at the root.
	AliasOnly bool `json:"aliasOnly,omitempty"`
	// Ignore names JSON keys, or attributes of OPML outlines, left out of
	// the comparison wherever they are: values that differ between two
	// installations by design.
	Ignore []string `json:"ignore,omitempty"`
}

type apiResponse struct {
	Name   string `json:"name"`
	Status int    `json:"status"`
	Body   string `json:"body"`
}

var apiPasswords = map[string]string{"alice": "alice-api-password", "bob": "bob-api-password"}

// session is what the cases of one user refer to by name.
type session struct {
	auth, token string
	// entries and feeds are identifiers by title.
	entries map[string]int64
	feeds   map[string]int64
	all     []int64
}

// replay sends the cases to the API at base, in order.
type replay struct {
	t        *testing.T
	base     string
	sessions map[string]*session
	answers  map[string]apiResponse
}

func (p *replay) do(method, rawURL, auth, contentType, body string) (int, string) {
	p.t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	return resp.StatusCode, string(data)
}

func (p *replay) get(s *session, pathAndQuery string, into any) {
	p.t.Helper()
	status, body := p.do(http.MethodGet, p.base+pathAndQuery, "GoogleLogin auth="+s.auth, "", "")
	if status != http.StatusOK {
		p.t.Fatalf("GET %s: status %d: %s", pathAndQuery, status, body)
	}
	if into != nil {
		if err := json.Unmarshal([]byte(body), into); err != nil {
			p.t.Fatalf("GET %s: %v in %s", pathAndQuery, err, body)
		}
	}
}

// session logs the user in and learns the identifiers of feeds and entries
// through the API itself.
func (p *replay) session(user string) *session {
	p.t.Helper()
	if s := p.sessions[user]; s != nil {
		return s
	}
	s := &session{entries: map[string]int64{}, feeds: map[string]int64{}}
	status, body := p.do(http.MethodPost, p.base+"/accounts/ClientLogin", "", "application/x-www-form-urlencoded",
		url.Values{"Email": {user}, "Passwd": {apiPasswords[user]}}.Encode())
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "Auth="); ok {
			s.auth = strings.TrimSpace(v)
		}
	}
	if s.auth == "" {
		p.t.Fatalf("login of %s: status %d: %q", user, status, body)
	}
	_, token := p.do(http.MethodGet, p.base+"/reader/api/0/token", "GoogleLogin auth="+s.auth, "", "")
	s.token = strings.TrimSpace(token)

	var list struct {
		Subscriptions []struct{ ID, Title string }
	}
	p.get(s, "/reader/api/0/subscription/list?output=json", &list)
	for _, sub := range list.Subscriptions {
		id, err := strconv.ParseInt(strings.TrimPrefix(sub.ID, "feed/"), 10, 64)
		if err != nil {
			p.t.Fatalf("subscription %q: %v", sub.ID, err)
		}
		s.feeds[sub.Title] = id
		var stream struct {
			Items []struct{ TimestampUsec, Title string }
		}
		p.get(s, "/reader/api/0/stream/contents/"+sub.ID+"?n=1000", &stream)
		for _, it := range stream.Items {
			id, err := strconv.ParseInt(it.TimestampUsec, 10, 64)
			if err != nil {
				p.t.Fatalf("entry %q: %v", it.Title, err)
			}
			if _, twice := s.entries[it.Title]; twice {
				p.t.Fatalf("user %s has two entries titled %q; cases name entries by title", user, it.Title)
			}
			s.entries[it.Title] = id
			s.all = append(s.all, id)
		}
	}
	p.sessions[user] = s
	return s
}

var placeholder = regexp.MustCompile(`\{(token|all|(item|hex|id|sec|sec\+1|sec-1|feed|cont):([^}]*))\}`)

// expand fills in the placeholders of a case:
//
//	{token}        the token for changes
//	{all}          i=<id> for every entry the user had at the start, joined by &
//	{item:Title}   the long identifier of the entry with that title
//	{hex:Title}    its identifier as 16 hexadecimal digits
//	{id:Title}     its identifier in decimal
//	{sec:Title}    the second it was added at; {sec+1:…} and {sec-1:…} the ones around
//	{feed:Title}   the identifier of the feed with that title
//	{cont:case}    the continuation in the answer to the case of that name
func (p *replay) expand(s *session, text string) string {
	p.t.Helper()
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		parts := placeholder.FindStringSubmatch(m)
		switch parts[1] {
		case "token":
			return s.token
		case "all":
			pairs := make([]string, len(s.all))
			for i, id := range s.all {
				pairs[i] = "i=" + strconv.FormatInt(id, 10)
			}
			return strings.Join(pairs, "&")
		}
		kind, name := parts[2], parts[3]
		switch kind {
		case "feed":
			id, ok := s.feeds[name]
			if !ok {
				p.t.Fatalf("no feed titled %q", name)
			}
			return strconv.FormatInt(id, 10)
		case "cont":
			var answer struct{ Continuation string }
			if err := json.Unmarshal([]byte(p.answers[name].Body), &answer); err != nil || answer.Continuation == "" {
				p.t.Fatalf("no continuation in the answer to %q: %s", name, p.answers[name].Body)
			}
			return answer.Continuation
		}
		id, ok := s.entries[name]
		if !ok {
			p.t.Fatalf("no entry titled %q", name)
		}
		switch kind {
		case "item":
			return fmt.Sprintf("tag:google.com,2005:reader/item/%016x", id)
		case "hex":
			return fmt.Sprintf("%016x", id)
		case "sec":
			return strconv.FormatInt(id/1_000_000, 10)
		case "sec+1":
			return strconv.FormatInt(id/1_000_000+1, 10)
		case "sec-1":
			return strconv.FormatInt(id/1_000_000-1, 10)
		}
		return strconv.FormatInt(id, 10)
	})
}

func (p *replay) run(c apiCase) apiResponse {
	p.t.Helper()
	user := c.User
	if user == "" {
		user = "alice"
	}
	s := p.session(user)
	auth := "GoogleLogin auth=" + s.auth
	switch {
	case c.Auth == "none":
		auth = ""
	case c.Auth == "bad":
		auth = "GoogleLogin auth=" + user + "/0000000000000000000000000000000000000000"
	case strings.HasPrefix(c.Auth, "raw:"):
		auth = strings.TrimPrefix(c.Auth, "raw:")
	}
	target := p.base + p.expand(s, c.Path)
	if c.Query != "" {
		target += "?" + p.expand(s, c.Query)
	}
	method := c.Method
	if method == "" {
		method = http.MethodGet
	}
	contentType := c.ContentType
	if contentType == "" {
		contentType = "application/x-www-form-urlencoded"
	}
	status, body := p.do(method, target, auth, contentType, p.expand(s, c.Body))
	answer := apiResponse{Name: c.Name, Status: status, Body: body}
	p.answers[c.Name] = answer
	return answer
}

func readJSON(t *testing.T, file string, into any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

// Keys every comparison leaves out: the time of the answer, and the address
// of the icon, which freshgo builds its own way.
var alwaysIgnored = []string{"updated", "iconUrl"}

// strip removes the named keys from a decoded JSON value, at any depth. It
// also puts the categories of an item in one order: FreshRSS lists the
// labels of an entry in the order the database happens to return them.
func strip(v any, keys map[string]bool) any {
	switch value := v.(type) {
	case map[string]any:
		for k, inner := range value {
			if keys[k] {
				delete(value, k)
				continue
			}
			value[k] = strip(inner, keys)
		}
		if list, ok := value["categories"].([]any); ok {
			slices.SortFunc(list, func(a, b any) int {
				x, _ := a.(string)
				y, _ := b.(string)
				return strings.Compare(x, y)
			})
		}
	case []any:
		for i, inner := range value {
			value[i] = strip(inner, keys)
		}
	}
	return v
}

// outlines reads an OPML document into its outlines: attributes and
// children, without the head, which carries the date, and without the
// attributes named in ignore.
func outlines(doc string, ignore []string) (any, error) {
	type outline struct {
		Attrs    map[string]string
		Children []*outline
	}
	dec := xml.NewDecoder(strings.NewReader(doc))
	root := &outline{}
	stack := []*outline{root}
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "outline" {
				continue
			}
			o := &outline{Attrs: map[string]string{}}
			for _, a := range t.Attr {
				if !slices.Contains(ignore, a.Name.Local) {
					o.Attrs[a.Name.Space+":"+a.Name.Local] = a.Value
				}
			}
			stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, o)
			stack = append(stack, o)
		case xml.EndElement:
			if t.Name.Local == "outline" {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(root.Children) == 0 {
		return nil, errors.New("no outlines")
	}
	return root.Children, nil
}

// same reports whether two answers say the same thing: as JSON or as OPML
// when both are, as text otherwise.
func same(got, want string, ignore []string) bool {
	var g, w any
	if json.Unmarshal([]byte(got), &g) == nil && json.Unmarshal([]byte(want), &w) == nil {
		keys := map[string]bool{}
		for _, k := range append(ignore, alwaysIgnored...) {
			keys[k] = true
		}
		return reflect.DeepEqual(strip(g, keys), strip(w, keys))
	}
	if gotOutlines, err := outlines(got, ignore); err == nil {
		if wo, err := outlines(want, ignore); err == nil {
			return reflect.DeepEqual(gotOutlines, wo)
		}
	}
	return strings.TrimSpace(got) == strings.TrimSpace(want)
}

// freshgoAPI imports the reference installation into the database and
// serves the API over it. The feeds of the reference live on
// http://feeds.freshgo.test/; requests for them are sent to a local server
// that answers with the corpus file of the same name.
func freshgoAPI(t *testing.T, db *store.Store) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	if _, err := importer.Run(ctx, db, importer.Options{DataDir: filepath.Join(referenceDir, "sqlite", "data")}); err != nil {
		t.Fatalf("import: %v", err)
	}
	feeds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "feeds.freshgo.test" {
			http.Error(w, "not asked as a proxy", http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(filepath.Join(referenceDir, "corpus", path.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(feeds.Close)
	proxy, err := url.Parse(feeds.URL)
	if err != nil {
		t.Fatal(err)
	}
	registry := &hooks.Registry{}
	registry.FetchBefore.Add(0, func(_ context.Context, f hooks.Fetch) bool {
		f.Request.Params.Proxy = proxy
		return false
	})
	client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httptest.NewServer(New(Options{
		DB: db, Refresher: refresh.New(db, client, registry, log), Hooks: registry, Log: log,
		BaseURL: "http://freshgo.test",
	}))
	t.Cleanup(api.Close)
	return api
}

// R2 and R7: freshgo answers the requests of testdata/reference/api/cases.json
// the way FreshRSS 1.30.1 answered them over the same data, at the root and
// under the path FreshRSS serves the API at.
func TestReferenceAPI(t *testing.T) {
	var cases []apiCase
	readJSON(t, casesFile, &cases)

	if base := os.Getenv(envRecord); base != "" {
		p := &replay{t: t, base: strings.TrimRight(base, "/") + Alias, sessions: map[string]*session{}, answers: map[string]apiResponse{}}
		recorded := make([]apiResponse, 0, len(cases))
		for _, c := range cases {
			recorded = append(recorded, p.run(c))
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "\t")
		if err := enc.Encode(recorded); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(responsesFile, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d answers of %s", len(recorded), base)
		return
	}

	var recorded []apiResponse
	readJSON(t, responsesFile, &recorded)
	want := map[string]apiResponse{}
	for _, r := range recorded {
		want[r.Name] = r
	}
	for _, e := range storetest.Engines() {
		for _, prefix := range []string{"", Alias} {
			name := e.Name + "/root"
			if prefix != "" {
				name = e.Name + "/alias"
			}
			t.Run(name, func(t *testing.T) {
				driver, dsn := e.New(t)
				db, err := store.Open(context.Background(), driver, dsn)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				api := freshgoAPI(t, db)
				p := &replay{t: t, base: api.URL + prefix, sessions: map[string]*session{}, answers: map[string]apiResponse{}}
				for _, c := range cases {
					if c.AliasOnly && prefix == "" {
						continue
					}
					expected, ok := want[c.Name]
					if !ok {
						t.Fatalf("case %q has no recorded answer; run testdata/reference/api/generate.sh", c.Name)
					}
					got := p.run(c)
					if got.Status != expected.Status {
						t.Errorf("%s: status %d, FreshRSS answered %d\n freshgo: %s\nFreshRSS: %s",
							c.Name, got.Status, expected.Status, got.Body, expected.Body)
						continue
					}
					if !same(got.Body, expected.Body, c.Ignore) {
						t.Errorf("%s:\n freshgo: %s\nFreshRSS: %s", c.Name, got.Body, expected.Body)
					}
				}
			})
		}
	}
}
