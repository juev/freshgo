package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
)

// Ways a sharing service wants the link and the title written into its
// address: as they are, as PHP rawurlencode writes them, or as urlencode does.
const (
	encodeNone = iota
	encodeRaw
	encodeQuery
)

// shareService is a place an entry can be sent to: an entry of app/shares.php
// of FreshRSS at commit 219eaf58.
type shareService struct {
	// id is what the settings of a user name the service by.
	id   string
	name string
	// address is where the entry goes: ~LINK~ and ~TITLE~ stand for the
	// entry, ~ID~ for its identifier, ~URL~ for the address of the service
	// the user gave.
	address string
	encode  int
	// advanced is a service the user has to give the address of; button
	// one that is not an address at all but something the browser does.
	advanced bool
	button   bool
	help     string
}

var shareServices = []shareService{
	{"archiveORG", "archive.org", "https://web.archive.org/save/~LINK~", encodeNone, false, false, "https://web.archive.org"},
	{"archiveIS", "archive.is", "https://archive.is/submit/?url=~LINK~", encodeNone, false, false, "https://archive.is/"},
	{"archivePH", "archive.ph", "https://archive.ph/submit/?url=~LINK~", encodeNone, false, false, "https://archive.ph/"},
	{"bluesky", "Bluesky", "https://bsky.app/intent/compose?text=~LINK~", encodeQuery, false, false, ""},
	{"buffer", "Buffer", "https://publish.buffer.com/compose?url=~LINK~&text=~TITLE~", encodeRaw, false, false, "https://support.buffer.com/en-us/articles/scheduling-posts-4Qdld7giAZ"},
	{"clipboard", "Clipboard", "~LINK~", encodeNone, false, true, ""},
	{"diaspora", "Diaspora*", "~URL~/bookmarklet?url=~LINK~&title=~TITLE~", encodeRaw, true, false, "https://diasporafoundation.org/"},
	{"email", "Email", "mailto:?subject=~TITLE~&body=~LINK~", encodeRaw, false, false, ""},
	{"email-webmail-firefox-fix", "Email (webmail - fix for Firefox)", "mailto:?subject=~TITLE~&body=~LINK~", encodeRaw, false, false, ""},
	{"facebook", "Facebook", "https://www.facebook.com/sharer.php?u=~LINK~&t=~TITLE~", encodeRaw, false, false, ""},
	{"gnusocial", "GNU social", "~URL~/notice/new?content=~TITLE~%20~LINK~", encodeQuery, true, false, "https://gnusocial.rocks/"},
	{"jdh", "Journal du hacker", "https://www.journalduhacker.net/stories/new?url=~LINK~&title=~TITLE~", encodeRaw, false, false, ""},
	{"Known", "Known based sites", "~URL~/share?share_url=~LINK~&share_title=~TITLE~", encodeRaw, true, false, "https://withknown.com/"},
	{"lemmy", "Lemmy", "~URL~/create_post?url=~LINK~&title=~TITLE~", encodeRaw, true, false, "https://join-lemmy.org/"},
	{"linkace", "LinkAce", "~URL~/bookmarklet/add?u=~LINK~&t=~TITLE~", encodeRaw, true, false, "https://www.linkace.org/"},
	{"linkding", "Linkding", "~URL~/bookmarks/new?url=~LINK~&title=~TITLE~&auto_close", encodeRaw, true, false, "https://linkding.link/how-to/"},
	{"linkedin", "LinkedIn", "https://www.linkedin.com/shareArticle?url=~LINK~&title=~TITLE~&source=FreshRSS", encodeRaw, false, false, ""},
	{"mastodon", "Mastodon", "~URL~/share?title=~TITLE~&url=~LINK~", encodeRaw, true, false, "https://joinmastodon.org/"},
	{"movim", "Movim", "~URL~/?share/~LINK~", encodeQuery, true, false, "https://movim.eu/"},
	{"nextcloud-bookmarks", "Nextcloud Bookmarks", "~URL~/apps/bookmarks/bookmarklet?url=~LINK~&title=~TITLE~", encodeRaw, true, false, "https://github.com/nextcloud/bookmarks"},
	{"omnivore", "Omnivore", "~URL~/api/save?url=~LINK~", encodeQuery, true, false, "https://github.com/omnivore-app/omnivore"},
	{"pinboard", "Pinboard", "https://pinboard.in/add?next=same&url=~LINK~&title=~TITLE~", encodeQuery, false, false, "https://pinboard.in/api/"},
	{"pinterest", "Pinterest", "https://www.pinterest.com/pin/create/button/?url=~LINK~", encodeRaw, false, false, "https://www.pinterest.com/"},
	{"print", "Print", "#", encodeNone, false, true, ""},
	{"raindrop", "Raindrop.io", "https://app.raindrop.io/add?link=~LINK~&title=~TITLE~", encodeRaw, false, false, ""},
	{"reddit", "Reddit", "https://www.reddit.com/submit?url=~LINK~", encodeRaw, false, false, "https://www.reddit.com/wiki/submitting?v=c2ae883a-04b9-11e4-a68c-12313b01a1fc"},
	{"shaarli", "Shaarli", "~URL~?post=~LINK~&title=~TITLE~&source=FreshRSS", encodeRaw, true, false, "https://sebsauvage.net/wiki/doku.php?id=php:shaarli"},
	{"telegram", "Telegram", "https://t.me/share/url?url=~LINK~&text=~TITLE~", encodeRaw, false, false, ""},
	{"twitter", "Twitter", "https://twitter.com/share?url=~LINK~&text=~TITLE~", encodeRaw, false, false, ""},
	{"wallabag", "wallabag v1", "~URL~?action=add&url=~LINK~", encodeRaw, true, false, "https://wallabag.org/"},
	{"wallabagv2", "wallabag v2", "~URL~/bookmarklet?url=~LINK~", encodeRaw, true, false, "https://wallabag.org/"},
	{"web-sharing-api", "System sharing", "~LINK~", encodeNone, false, true, ""},
	{"whatsapp", "Whatsapp", "https://wa.me/?text=~TITLE~%20|%20~LINK~", encodeRaw, false, false, "https://faq.whatsapp.com/iphone/how-to-link-to-whatsapp-from-a-different-app/?lang=en"},
	{"xing", "Xing", "https://www.xing.com/spi/shares/new?url=~LINK~", encodeRaw, false, false, "https://dev.xing.com/plugins/share_button/docs"},
}

func serviceByID(id string) *shareService {
	for i := range shareServices {
		if shareServices[i].id == id {
			return &shareServices[i]
		}
	}
	return nil
}

// target is the address the service opens for an entry. base is the
// address of the service for those that need one.
func (s *shareService) target(base, id, title, link string) string {
	encode := func(text string) string {
		switch s.encode {
		case encodeRaw:
			return strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
		case encodeQuery:
			// PHP writes a tilde as an escape there, Go does not.
			return strings.ReplaceAll(url.QueryEscape(text), "~", "%7E")
		}
		return text
	}
	// One after the other, as FreshRSS does: a title that spells a
	// placeholder gets it filled in.
	address := strings.ReplaceAll(s.address, "~ID~", encode(id))
	address = strings.ReplaceAll(address, "~URL~", base)
	address = strings.ReplaceAll(address, "~TITLE~", encode(title))
	return strings.ReplaceAll(address, "~LINK~", encode(link))
}

// sharing is a service the user has switched on, an element of the setting
// "sharing" as FreshRSS keeps it.
type sharing struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Token is the key of the API of the service, for the one service the
	// server sends entries to itself: Linkding. FreshRSS has no such field.
	Token string `json:"token"`
}

func readSharing(s attrs) []sharing {
	return phpList[sharing](s["sharing"])
}

// phpList reads a list FreshRSS keeps as a PHP array. Import makes a JSON
// array of one whose keys count from zero and an object of any other, which
// is what removing a service in FreshRSS leaves; the values of an object are
// taken by ascending key. Anything else is an empty list.
func phpList[T any](raw json.RawMessage) []T {
	var list []T
	if json.Unmarshal(raw, &list) == nil || len(list) > 0 {
		return list
	}
	var keyed map[string]T
	_ = json.Unmarshal(raw, &keyed)
	keys := make([]int, 0, len(keyed))
	for key := range keyed {
		if n, err := strconv.Atoi(key); err == nil {
			keys = append(keys, n)
		}
	}
	slices.Sort(keys)
	for _, n := range keys {
		list = append(list, keyed[strconv.Itoa(n)])
	}
	return list
}

// shareLink is a way to share an entry, as its menu offers it.
type shareLink struct {
	Name string
	// URL is where the link leads; Button names what the script does in
	// place of a link: "clipboard", "print" or "web-sharing-api".
	URL    string
	Button string
	// Save, when not empty, is the place of the service among those of the
	// user: the server saves the entry there itself, and for whoever may
	// change the entry the way is a form in place of the link.
	Save string
}

// shareLinks are the ways the user has switched on, for one entry.
//
// saves says the server can send entries to the services that take them.
func shareLinks(list []sharing, id int64, title, link string, saves bool) []shareLink {
	if link == "" {
		return nil
	}
	var out []shareLink
	for n, one := range list {
		service := serviceByID(one.Type)
		if service == nil || service.advanced && one.URL == "" {
			continue
		}
		name := one.Name
		if name == "" {
			name = service.name
		}
		if service.button {
			out = append(out, shareLink{Name: name, Button: service.id})
			continue
		}
		address := service.target(strings.TrimSuffix(one.URL, "/"), strconv.FormatInt(id, 10), title, link)
		// Only what a browser may be sent to: the address of a service comes
		// from the settings, and from an import.
		if parsed, err := url.Parse(address); err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "mailto" {
			continue
		}
		way := shareLink{Name: name, URL: address}
		if saves && one.saves() {
			way.Save = strconv.Itoa(n)
		}
		out = append(out, way)
	}
	return out
}

// linkding is the one service the server sends entries to itself, through
// the API of the service, when the user gave a token for it.
const linkding = "linkding"

// saves reports whether entries are saved to the service by the server.
func (s sharing) saves() bool {
	return s.Type == linkding && s.Token != "" && s.URL != ""
}

// Outcomes of saving an entry to a service that are told apart for the reader.
var (
	errServiceRefused = errors.New("the service refused the token")
	errServiceAnswer  = errors.New("the service did not take the entry")
)

// saveBookmark has the Linkding at base keep link as a bookmark. Linkding
// reads the title and the rest from the page, and updates the bookmark it
// already has for the address.
func (h *Handler) saveBookmark(ctx context.Context, base, token, link string) error {
	body, err := json.Marshal(map[string]string{"url": link})
	if err != nil {
		return err
	}
	_, err = h.services.Fetch(ctx, fetch.Request{
		URL:    strings.TrimSuffix(base, "/") + "/api/bookmarks/",
		Accept: fetch.AcceptJSON,
		Params: fetch.Params{
			Post: true, PostBody: string(body),
			Header: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Token " + token}},
		},
	})
	var status *fetch.StatusError
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &status):
		return err
	// A bookmark that was made is answered with 201, which the client
	// takes for an error as it does every status but 200.
	case status.Code/100 == 2:
		return nil
	case status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden:
		return fmt.Errorf("%w: %w", errServiceRefused, err)
	default:
		return fmt.Errorf("%w: %w", errServiceAnswer, err)
	}
}

// webLink reports whether a link of an entry is an http or https address.
func webLink(link string) bool {
	address, err := url.Parse(link)
	return err == nil && (address.Scheme == "http" || address.Scheme == "https")
}

// saveEntry sends an entry to the service of the user the form names by its
// place, which is one the server saves to itself.
func (h *Handler) saveEntry(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	e, ok := h.ownEntry(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	list := readSharing(userSettings(r))
	n, err := strconv.Atoi(r.PostForm.Get("service"))
	if h.services == nil || err != nil || n < 0 || n >= len(list) || !list[n].saves() || !webLink(e.Link) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	notice := "notice.linkding-saved"
	switch err := h.saveBookmark(ctx, list[n].URL, list[n].Token, e.Link); {
	case ctx.Err() != nil:
		return
	case errors.Is(err, errServiceRefused):
		notice = "notice.linkding-refused"
	case err != nil:
		// The error of a request names its address, never its headers.
		h.log.Warn("entry was not saved to the service", "user", user.Name, "entry", e.ID, "service", list[n].Type, "error", err)
		notice = "notice.linkding-failed"
	}
	h.back(w, r, e.ID, notice)
}

// integrationRow is a service the user has switched on, in the form.
type integrationRow struct {
	N        int
	Type     string
	Service  string
	Name     string
	URL      string
	Advanced bool
	Help     string
	// Token says the service takes a token of its API; HasToken that one
	// is stored. The token itself is not shown again.
	Token    bool
	HasToken bool
}

// integrationsPage is what the page of integrations shows.
type integrationsPage struct {
	Rows     []integrationRow
	Services []option
	Problem  string
}

func (h *Handler) showIntegrations(w http.ResponseWriter, r *http.Request, status int, list []sharing, problem string) {
	v := h.settingsView(r, "integrations")
	page := integrationsPage{}
	for n, one := range list {
		service := serviceByID(one.Type)
		if service == nil {
			continue
		}
		page.Rows = append(page.Rows, integrationRow{
			N: n, Type: one.Type, Service: service.name, Name: one.Name, URL: one.URL, Advanced: service.advanced, Help: service.help,
			Token: one.Type == linkding, HasToken: one.Token != "",
		})
	}
	page.Services = []option{{"", v.T("integrations.choose"), true}}
	for _, service := range shareServices {
		page.Services = append(page.Services, option{Value: service.id, Name: service.name})
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "integrations", v)
}

func (h *Handler) integrationsPage(w http.ResponseWriter, r *http.Request) {
	h.showIntegrations(w, r, http.StatusOK, readSharing(userSettings(r)), "")
}

// serviceAddress reads the address of a service a user gives: an http or
// https one, without what follows its path.
func serviceAddress(text string) (string, bool) {
	text = strings.TrimSpace(text)
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return text, false
	}
	return text, true
}

// saveIntegrations stores the services the form lists: those it keeps,
// with their names and addresses, and one more when it names one.
func (h *Handler) saveIntegrations(w http.ResponseWriter, r *http.Request) {
	var shown []sharing
	h.saveSettings(w, r, "/settings/integrations", func(form url.Values, s attrs) string {
		// The services are kept as objects: FreshRSS has fields freshgo
		// does not show.
		stored := phpList[attrs](s["sharing"])
		problem := ""
		kept := []attrs{}
		add := func(one attrs, kind, name, address, token string, forget bool) {
			service := serviceByID(kind)
			if service == nil {
				return
			}
			one.set("type", kind)
			if name = strings.TrimSpace(name); name == "" {
				name = service.name
			}
			one.set("name", name)
			delete(one, "url")
			if service.advanced {
				address, ok := serviceAddress(address)
				if !ok {
					problem = "integrations.problem.address"
				}
				one.set("url", address)
			}
			// An empty field keeps the token there is: it is not shown.
			switch token = strings.TrimSpace(token); {
			case kind != linkding || forget:
				delete(one, "token")
			case token != "":
				one.set("token", token)
			}
			kept = append(kept, one)
		}
		for n, one := range stored {
			i := strconv.Itoa(n)
			if form.Get("remove-"+i) != "" {
				continue
			}
			add(one, one.text("type"), form.Get("name-"+i), form.Get("url-"+i), form.Get("token-"+i), form.Get("forget-"+i) != "")
		}
		if kind := form.Get("add_type"); kind != "" {
			add(attrs{}, kind, form.Get("add_name"), form.Get("add_url"), form.Get("add_token"), false)
		}
		s.set("sharing", kept)
		shown = readSharing(s)
		return problem
	}, func(status int, _ attrs, problem string) { h.showIntegrations(w, r, status, shown, problem) })
}
