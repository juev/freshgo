package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/store"
)

func (s *site) settings(user string) map[string]any {
	s.t.Helper()
	return decoded(s.t, s.user(user).Settings)
}

// form reads the form of a page that posts to an address.
func (s *site) formAt(target, action string) url.Values {
	s.t.Helper()
	body := s.shown(target)
	start := strings.Index(body, `action="`+action+`"`)
	if start < 0 {
		s.t.Fatalf("GET %s: no form that posts to %s\n%s", target, action, body)
	}
	end := strings.Index(body[start:], "</form>")
	return formValues(s.t, body[start:start+end])
}

// R11: the pages of settings store what their forms say under the names
// FreshRSS has for it, and what they store is what the interface goes by.
func TestUserSettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		for _, page := range []string{"display", "reading", "archiving", "keys", "privacy", "profile"} {
			body := s.shown("/settings/" + page)
			if !strings.Contains(body, `<a href="/settings/`+page+`" aria-current="page">`) || !strings.Contains(body, `<a href="/settings/display"`) ||
				!strings.Contains(body, `aria-current="page">Settings</a>`) {
				t.Errorf("GET /settings/%s: the menus do not mark the page", page)
			}
		}
		before := s.settings("alice")

		// Display: the form as shown changes nothing it shows.
		form := s.formAt("/settings/display", "/settings/display")
		for key, value := range map[string]string{"language": "ru", "timezone": " Europe/Moscow ", "darkMode": "dark", "content_width": "large", "look": "modern"} {
			form.Set(key, value)
		}
		form.Del("show_date")
		location, body := s.follow("/settings/display", form)
		if location != "/settings/display" || notice(body) != "Сохранено." || !strings.Contains(body, `<html lang="ru" data-theme="dark" data-look="modern" data-width="large">`) {
			t.Errorf("after saving the display: at %q, notice %q\n%.300s", location, notice(body), body)
		}
		got := s.settings("alice")
		for key, want := range map[string]any{"language": "ru", "timezone": "Europe/Moscow", "darkMode": "dark", "content_width": "large", "look": "modern", "topline_date": false, "topline_website": "full"} {
			if got[key] != want {
				t.Errorf("setting %s = %v, want %v", key, got[key], want)
			}
		}
		if got["passwordHash"] != before["passwordHash"] || !reflect.DeepEqual(got["shortcuts"], before["shortcuts"]) {
			t.Error("saving the display changed settings it does not show")
		}
		if list := s.page("/?state=all"); strings.Contains(list, `<time datetime=`) && strings.Contains(list[strings.Index(list, `<summary>`):strings.Index(list, `</summary>`)], "<time") {
			t.Error("the row of an entry shows the date the reader turned off")
		}
		form.Set("timezone", "Mars/Olympus")
		form.Set("language", "en")
		if a := s.post("/settings/display", form); a.status != http.StatusBadRequest || !strings.Contains(a.body, `value="Mars/Olympus"`) || s.settings("alice")["language"] != "ru" {
			t.Errorf("a time zone there is not: status %d", a.status)
		}
		form.Set("timezone", "")
		s.follow("/settings/display", form)

		// Reading.
		form = s.formAt("/settings/reading", "/settings/reading")
		for key, value := range map[string]string{
			"posts_per_page": "7", "default_view": "all", "sort": "title", "sort_order": "ASC", "display_posts": "1",
			"mark_reception": "1", "mark_gone": "1", "max_n_unread_on": "1", "max_n_unread": "30",
			"same_title_in_feed_on": "1", "same_title_in_feed": "5", "mark_updated_article_unread": "1",
			"filters_read": "intitle:ad", "filters_star": "author:me\nintitle:ad",
		} {
			form.Set(key, value)
		}
		for _, key := range []string{"auto_load_more", "hide_read_feeds", "mark_article"} {
			form.Del(key)
		}
		s.follow("/settings/reading", form)
		got = s.settings("alice")
		when := got["mark_when"].(map[string]any)
		if got["posts_per_page"] != 7.0 || got["default_view"] != "all" || got["sort"] != "title" || got["sort_order"] != "ASC" ||
			got["display_posts"] != true || got["auto_load_more"] != false || got["hide_read_feeds"] != false ||
			got["mark_updated_article_unread"] != true || when["article"] != false || when["reception"] != true || when["gone"] != true ||
			when["max_n_unread"] != 30.0 || when["same_title_in_feed"] != 5.0 ||
			!reflect.DeepEqual(got["filters"], []any{
				map[string]any{"search": "intitle:ad", "actions": []any{"read", "star"}},
				map[string]any{"search": "author:me", "actions": []any{"star"}},
			}) {
			t.Errorf("settings after the form of reading = %v", got)
		}
		// The other ways FreshRSS marks entries read stay as they were.
		if beforeWhen := before["mark_when"].(map[string]any); when["scroll"] != beforeWhen["scroll"] || when["site"] != beforeWhen["site"] {
			t.Errorf("mark_when lost what the form does not show: %v, was %v", when, beforeWhen)
		}
		list := s.page("/")
		if n := len(listed(list)); n != 7 || !strings.Contains(list, `<option value="title" selected>`) || !strings.Contains(list, `<details open>`) {
			t.Errorf("the reading screen lists %d entries a page after the settings said 7, sorted by title, open", n)
		}
		if config := scriptConfigOf(t, list); config.MarkOnOpen || config.AutoLoad {
			t.Errorf("the script is told to mark on opening (%v) and to load more (%v) against the settings", config.MarkOnOpen, config.AutoLoad)
		}
		shown := s.formAt("/settings/reading", "/settings/reading")
		if shown.Get("max_n_unread") != "30" || shown.Get("same_title_in_feed_on") != "1" || shown.Get("filters_star") != "intitle:ad\nauthor:me" || shown.Get("mark_article") != "" {
			t.Errorf("form of reading after the change = %v", shown)
		}
		shown.Set("posts_per_page", "0")
		if a := s.post("/settings/reading", shown); a.status != http.StatusBadRequest || s.settings("alice")["posts_per_page"] != 7.0 {
			t.Errorf("no entries a page: status %d", a.status)
		}

		// Archiving.
		form = s.formAt("/settings/archiving", "/settings/archiving")
		for key, value := range map[string]string{"ttl_default": "7200", "keep_max": "10", "keep_max_on": "1", "keep_min": "2", "keep_period_count": "5", "keep_period_unit": "P1D", "keep_period_on": "1"} {
			form.Set(key, value)
		}
		form.Del("keep_favourites")
		s.follow("/settings/archiving", form)
		got = s.settings("alice")
		if archiving := got["archiving"].(map[string]any); got["ttl_default"] != 7200.0 || archiving["keep_period"] != "P5D" || archiving["keep_max"] != 10.0 ||
			archiving["keep_min"] != 2.0 || archiving["keep_favourites"] != false {
			t.Errorf("settings after the form of archiving: ttl %v, archiving %v", got["ttl_default"], got["archiving"])
		}
		// Purging applies the rules now: no feed keeps more than ten entries
		// it could give up.
		entries, err := s.db.CountEntries(context.Background(), s.user("alice").ID)
		if err != nil {
			t.Fatal(err)
		}
		_, body = s.follow("/settings/archiving/purge", nil)
		after, _ := s.db.CountEntries(context.Background(), s.user("alice").ID)
		if want := fmt.Sprintf("%d", entries-after); !strings.HasPrefix(notice(body), want+" ") || after > entries {
			t.Errorf("after purging: notice %q, %d entries of %d left", notice(body), after, entries)
		}

		// Privacy: the frames of the hosts named are told where the reader comes from.
		s.follow("/settings/privacy", url.Values{"hosts": {" Video.Example \n\nplayer.example"}})
		if got := s.settings("alice")["send_referrer_allowlist"]; !reflect.DeepEqual(got, []any{"video.example", "player.example"}) {
			t.Errorf("send_referrer_allowlist = %v", got)
		}
		if a := s.post("/settings/privacy", url.Values{"hosts": {"https://video.example/x"}}); a.status != http.StatusBadRequest {
			t.Errorf("an address in place of a host: status %d", a.status)
		}
		frames := withReferrers(`<p>a</p><iframe src="https://video.example/1"></iframe><iframe src="https://other.example/2"></iframe>`, []string{"video.example"})
		if frames != `<p>a</p><iframe src="https://video.example/1" referrerpolicy="strict-origin-when-cross-origin"></iframe><iframe src="https://other.example/2"></iframe>` {
			t.Errorf("frames with the referrer policy = %s", frames)
		}
	})
}

// R11: the profile changes passwords, with the current one typed, and
// deletes the account.
func TestProfile(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		now := s.clock()
		s.asAlice()
		location, _ := s.follow("/settings/profile", url.Values{"email": {" alice@example.org "}, "token": {"s3cret-token"}})
		if got := s.settings("alice"); location != "/settings/profile" || got["mail_login"] != "alice@example.org" || got["token"] != "s3cret-token" {
			t.Errorf("profile after the form: %v %v", got["mail_login"], got["token"])
		}
		for name, form := range map[string]url.Values{"address": {"email": {"nobody"}}, "token": {"email": {""}, "token": {"with space"}}} {
			if a := s.post("/settings/profile", form); a.status != http.StatusBadRequest {
				t.Errorf("a bad %s: status %d", name, a.status)
			}
		}

		// Another browser of alice is logged in as well.
		other := &site{t: t, db: s.db, h: s.h}
		other.asAlice()
		old := s.settings("alice")["passwordHash"]
		for name, c := range map[string]struct {
			form   url.Values
			status int
		}{
			"wrong current": {url.Values{"password": {"guess"}, "new": {"new-password"}, "again": {"new-password"}}, http.StatusForbidden},
			"short":         {url.Values{"password": {"alice-web-password"}, "new": {"short"}, "again": {"short"}}, http.StatusBadRequest},
			"differs":       {url.Values{"password": {"alice-web-password"}, "new": {"new-password"}, "again": {"new-passwore"}}, http.StatusBadRequest},
			"long":          {url.Values{"password": {"alice-web-password"}, "new": {strings.Repeat("x", 73)}, "again": {strings.Repeat("x", 73)}}, http.StatusBadRequest},
		} {
			if a := s.post("/settings/profile/password", c.form); a.status != c.status || !strings.Contains(a.body, `role="alert"`) || s.settings("alice")["passwordHash"] != old {
				t.Errorf("%s: status %d, want %d and the password kept", name, a.status, c.status)
			}
		}
		_, body := s.follow("/settings/profile/password", url.Values{"password": {"alice-web-password"}, "new": {"new-password"}, "again": {"new-password"}})
		hash, _ := s.settings("alice")["passwordHash"].(string)
		if notice(body) != "Password changed." || bcrypt.CompareHashAndPassword([]byte(hash), []byte("new-password")) != nil {
			t.Errorf("after changing the password: notice %q", notice(body))
		}
		if a := other.get("/"); a.status != http.StatusSeeOther {
			t.Errorf("the other login of alice after the change of her password: status %d, want it ended", a.status)
		}
		if a := s.get("/settings/profile"); a.status != http.StatusOK {
			t.Errorf("the login that changed the password: status %d, want it kept", a.status)
		}

		// The password of apps.
		s.follow("/settings/profile/api-password", url.Values{"new": {"app-password"}})
		if hash := s.user("alice").APIPasswordHash; bcrypt.CompareHashAndPassword([]byte(hash), []byte("app-password")) != nil {
			t.Error("the password of apps was not stored")
		}
		if a := s.post("/settings/profile/api-password", url.Values{"new": {"short"}}); a.status != http.StatusBadRequest {
			t.Errorf("a short password of apps: status %d", a.status)
		}
		_, body = s.follow("/settings/profile/api-password", url.Values{"new": {""}})
		if s.user("alice").APIPasswordHash != "" || notice(body) != "Apps can no longer connect." {
			t.Errorf("after removing the password of apps: notice %q", notice(body))
		}

		// Guesses of the current password are slowed down like logins.
		guess := url.Values{"password": {"guess"}, "new": {"other-password"}, "again": {"other-password"}}
		for range freeFailures {
			s.post("/settings/profile/password", guess)
		}
		guess.Set("password", "new-password")
		if a := s.post("/settings/profile/password", guess); a.status != http.StatusTooManyRequests {
			t.Errorf("after %d wrong passwords: status %d, want 429", freeFailures, a.status)
		}
		*now = now.Add(time.Hour)
		// The default user stays; bob goes with everything he has.
		if a := s.post("/settings/profile/delete", url.Values{"password": {"new-password"}}); a.status != http.StatusBadRequest || !strings.Contains(a.body, "cannot be deleted") {
			t.Errorf("the default user deleting the account: status %d", a.status)
		}
		s.cookies = nil
		s.login("bob", "bob-web-password", nil)
		if a := s.post("/settings/profile/delete", url.Values{"password": {"wrong"}}); a.status != http.StatusForbidden {
			t.Errorf("deleting the account with a wrong password: status %d", a.status)
		}
		a := s.post("/settings/profile/delete", url.Values{"password": {"bob-web-password"}})
		if _, err := s.db.UserByName(ctx, "bob"); a.status != http.StatusSeeOther || err == nil {
			t.Errorf("deleting the account: status %d, the user is still there: %v", a.status, err == nil)
		}
		if a := s.get("/"); a.header.Get("Location") != "/login?next=%2F" {
			t.Errorf("after deleting the account the visitor is at %q", a.header.Get("Location"))
		}
	})
}

// R15: administrators manage users and the installation; nobody else does.
func TestAdministration(t *testing.T) {
	imported(t, Options{FetchAllowlist: []string{"10.0.0.0/8"}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}, func(t *testing.T, s *site) {
		ctx := context.Background()
		now := s.clock()
		pages := []string{"/admin/users", "/admin/users/bob", "/admin/system", "/admin/authentication"}
		s.login("bob", "bob-web-password", nil)
		for _, page := range pages {
			if a := s.get(page); a.status != http.StatusForbidden {
				t.Errorf("GET %s as a plain user: status %d, want 403", page, a.status)
			}
			if a := s.post(page, url.Values{"action": {"promote"}, "title": {"Mine"}, "auth_type": {"none"}}); a.status != http.StatusForbidden {
				t.Errorf("POST %s as a plain user: status %d, want 403", page, a.status)
			}
		}
		if strings.Contains(s.page("/"), "/admin/users") || !strings.Contains(s.page("/log"), "No records.") {
			t.Error("a plain user is shown the administration, or has no journal")
		}
		if system, _ := s.db.System(ctx); system.Title == "Mine" || system.AuthType != store.AuthForm || readPreferences(s.user("bob")).IsAdmin {
			t.Fatal("a plain user changed the installation")
		}
		s.cookies = nil
		s.asAlice()
		for _, page := range pages {
			if body := s.shown(page); !strings.Contains(body, `aria-current="page">Administration</a>`) {
				t.Errorf("GET %s: the menu does not mark the administration", page)
			}
		}
		if body := s.page("/admin/users"); !strings.Contains(body, `<th scope="row"><a href="/admin/users/alice">alice</a></th>`+"\n"+`<td>Default user, administrator</td>`+"\n"+`<td>8</td>`) {
			t.Errorf("the list of users does not show alice with her feeds:\n%s", body)
		}
		if body := s.page("/admin/system"); !strings.Contains(body, "<code>10.0.0.0/8</code>") {
			t.Error("the page of the system does not show the allowlist")
		}

		// Users.
		location, body := s.follow("/admin/users", url.Values{"name": {"carol"}, "new": {"carol-password"}, "again": {"carol-password"}, "admin": {"1"}})
		carol := s.user("carol")
		if location != "/admin/users" || notice(body) != "User added." || !readPreferences(carol).IsAdmin ||
			bcrypt.CompareHashAndPassword([]byte(readPreferences(carol).PasswordHash), []byte("carol-password")) != nil ||
			bcrypt.CompareHashAndPassword([]byte(carol.APIPasswordHash), []byte("carol-password")) != nil {
			t.Fatalf("after adding a user: at %q, notice %q, %s", location, notice(body), carol.Settings)
		}
		for name, form := range map[string]url.Values{
			"taken": {"name": {"bob"}, "new": {"some-password"}, "again": {"some-password"}}, "bad name": {"name": {"no spaces"}, "new": {"some-password"}, "again": {"some-password"}},
			"short": {"name": {"dave"}, "new": {"short"}, "again": {"short"}},
		} {
			if a := s.post("/admin/users", form); a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) {
				t.Errorf("adding a user, %s: status %d", name, a.status)
			}
		}
		bobBrowser := &site{t: t, db: s.db, h: s.h}
		bobBrowser.login("bob", "bob-web-password", nil)
		act := func(user, action string, more url.Values) answer {
			form := url.Values{"action": {action}}
			for key, values := range more {
				form[key] = values
			}
			return s.post("/admin/users/"+user, form)
		}
		if a := act("bob", "promote", nil); a.status != http.StatusSeeOther || !readPreferences(s.user("bob")).IsAdmin {
			t.Errorf("promoting: status %d", a.status)
		}
		if a := bobBrowser.get("/admin/users"); a.status != http.StatusOK {
			t.Errorf("the promoted user asking for the administration: status %d", a.status)
		}
		act("bob", "demote", nil)
		if readPreferences(s.user("bob")).IsAdmin {
			t.Error("demoting left the user an administrator")
		}
		act("bob", "disable", nil)
		if a := bobBrowser.get("/"); readPreferences(s.user("bob")).enabled() || a.status != http.StatusSeeOther {
			t.Errorf("a disabled user: status %d of a page asked for with the old login", a.status)
		}
		act("bob", "enable", nil)
		if a := act("bob", "password", url.Values{"new": {"bobs-new-password"}, "again": {"bobs-new-password"}}); a.status != http.StatusSeeOther {
			t.Errorf("setting a password: status %d", a.status)
		}
		if a := bobBrowser.login("bob", "bobs-new-password", nil); a.status != http.StatusSeeOther {
			t.Errorf("login with the password the administrator set: status %d", a.status)
		}
		if a := act("bob", "password", url.Values{"new": {"short"}, "again": {"short"}}); a.status != http.StatusBadRequest {
			t.Errorf("setting a short password: status %d", a.status)
		}
		// An administrator cannot lock themselves, or the default user, out.
		for _, action := range []string{"disable", "demote", "delete"} {
			if a := act("alice", action, nil); a.status != http.StatusBadRequest {
				t.Errorf("%s of one's own account: status %d, want 400", action, a.status)
			}
		}
		if a := act("bob", "explode", nil); a.status != http.StatusBadRequest {
			t.Errorf("an action there is not: status %d", a.status)
		}
		if a := act("nobody", "delete", nil); a.status != http.StatusNotFound {
			t.Errorf("deleting a user there is not: status %d", a.status)
		}
		location, body = s.follow("/admin/users/bob", url.Values{"action": {"delete"}})
		if _, err := s.db.UserByName(ctx, "bob"); location != "/admin/users" || notice(body) != "User deleted." || err == nil {
			t.Errorf("after deleting a user: at %q, notice %q", location, notice(body))
		}

		// The installation.
		form := s.formAt("/admin/system", "/admin/system")
		for key, value := range map[string]string{
			"title": "News desk", "language": "ru", "max_feeds": "50", "max_categories": "5", "max_registrations": "3",
			"cookie_days": "10", "reauth_minutes": "5", "closed_registration_message": " Ask the administrator. ",
			"proxy": " socks5h://bob:secret@127.0.0.1:1080 ", "media_proxy": "all",
		} {
			form.Set(key, value)
		}
		s.follow("/admin/system", form)
		system, _ := s.db.System(ctx)
		if system.Title != "News desk" || system.Language != "ru" || system.Limits != (store.Limits{CookieDuration: 864000, MaxFeeds: 50, MaxCategories: 5, MaxRegistrations: 3}) ||
			system.ReauthTime != 300 || system.ClosedRegistrationMessage != "Ask the administrator." || system.DefaultUser != "alice" ||
			system.Proxy != "socks5h://bob:secret@127.0.0.1:1080" || system.MediaProxy != "all" {
			t.Errorf("system after the form = %+v", system)
		}
		form.Set("media_proxy", "everything")
		s.follow("/admin/system", form)
		if system, _ := s.db.System(ctx); system.MediaProxy != "all" {
			t.Errorf("the images behind the server after a value there is not = %q", system.MediaProxy)
		}
		form.Set("media_proxy", "none")
		if body := s.page("/admin/system"); !strings.Contains(body, `name="proxy" value="socks5h://bob:secret@127.0.0.1:1080"`) {
			t.Errorf("the page of the installation does not show its proxy:\n%s", body)
		}
		for _, address := range []string{"127.0.0.1:1080", "socks4://127.0.0.1:1080"} {
			form.Set("proxy", address)
			a := s.post("/admin/system", form)
			if system, _ := s.db.System(ctx); a.status != http.StatusBadRequest || !strings.Contains(a.body, "The proxy has to be an address") || system.Proxy != "socks5h://bob:secret@127.0.0.1:1080" {
				t.Errorf("the proxy %q: status %d, stored %q", address, a.status, system.Proxy)
			}
		}
		form.Set("proxy", "")
		s.follow("/admin/system", form)
		if system, _ := s.db.System(ctx); system.Proxy != "" {
			t.Errorf("the proxy after it was taken away = %q", system.Proxy)
		}
		form.Set("max_feeds", "-1")
		if a := s.post("/admin/system", form); a.status != http.StatusBadRequest {
			t.Errorf("a negative limit: status %d", a.status)
		}
		s.follow("/admin/authentication", url.Values{"auth_type": {"form"}, "allow_anonymous": {"1"}, "allow_anonymous_refresh": {"1"}})
		system, _ = s.db.System(ctx)
		if !system.AllowAnonymous || !system.AllowAnonymousRefresh || system.APIEnabled || system.HTTPAuthAutoRegister || system.AuthType != store.AuthForm {
			t.Errorf("system after the form of signing in = %+v", system)
		}

		// A way of telling users apart that would not know the administrator
		// who asks for it is not switched on.
		for _, kind := range []string{store.AuthHTTP, "nonsense"} {
			a := s.post("/admin/authentication", url.Values{"auth_type": {kind}, "api_enabled": {"1"}})
			if system, _ := s.db.System(ctx); system.AuthType != store.AuthForm || (kind == store.AuthHTTP && (a.status != http.StatusBadRequest || !strings.Contains(a.body, "would not know you"))) {
				t.Errorf("switching to %q without a proxy that vouches: status %d, auth_type %q", kind, a.status, system.AuthType)
			}
		}
		form = url.Values{"auth_type": {store.AuthHTTP}, "api_enabled": {"1"}}
		r := httptest.NewRequest(http.MethodPost, "/admin/authentication", strings.NewReader(form.Encode()))
		r.RemoteAddr = "127.0.0.1:4000"
		vouched := s.send(r, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin", "Remote-User": "alice"})
		if system, _ := s.db.System(ctx); vouched.status != http.StatusSeeOther || system.AuthType != store.AuthHTTP {
			t.Errorf("switching to the reverse proxy that vouches for the administrator: status %d, auth_type %q", vouched.status, system.AuthType)
		}
		s.system(func(system *store.System) { system.AuthType = store.AuthForm })
		form = s.formAt("/admin/system", "/admin/system")

		// After a while administration asks for the password again; the
		// rest of the interface does not.
		*now = now.Add(6 * time.Minute)
		a := s.get("/admin/system")
		if a.status != http.StatusSeeOther || a.header.Get("Location") != "/reauth?next=%2Fadmin%2Fsystem" {
			t.Fatalf("an administrator after the time to type the password again: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if a := s.post("/admin/system", form); a.header.Get("Location") != "/reauth?next=%2Fadmin%2Fusers" {
			t.Errorf("a form of the administration after that time: Location %q", a.header.Get("Location"))
		}
		if system, _ := s.db.System(ctx); system.Limits.MaxFeeds != 50 {
			t.Error("a form of the administration was stored without the password typed again")
		}
		// The journal of everybody is the administration's too.
		if a := s.get("/log?all=1"); a.header.Get("Location") != "/reauth?next=%2Flog%3Fall%3D1" {
			t.Errorf("the journal of everybody at that time: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if err := s.db.AddLog(ctx, &store.Log{Time: 1, User: "carol", Level: "WARN", Message: "kept"}); err != nil {
			t.Fatal(err)
		}
		if a := s.post("/log/clear", url.Values{"all": {"1"}}); a.status != http.StatusSeeOther || !strings.HasPrefix(a.header.Get("Location"), "/reauth") {
			t.Errorf("clearing the journal of everybody at that time: status %d", a.status)
		}
		if logs, _ := s.db.Logs(ctx, store.LogQuery{}); len(logs) != 1 {
			t.Error("the journal of everybody was cleared without the password typed again")
		}
		if a := s.get("/log"); a.status != http.StatusOK {
			t.Errorf("one's own journal at that time: status %d", a.status)
		}
		s.shown("/reauth?next=%2Fadmin%2Fsystem")
		if a := s.get("/settings/display"); a.status != http.StatusOK {
			t.Errorf("a page of settings at that time: status %d", a.status)
		}
		if a := s.post("/reauth", url.Values{"password": {"wrong"}, "next": {"/admin/system"}}); a.status != http.StatusForbidden {
			t.Errorf("a wrong password typed again: status %d", a.status)
		}
		if a := s.post("/reauth", url.Values{"password": {"alice-web-password"}, "next": {"/admin/system"}}); a.header.Get("Location") != "/admin/system" {
			t.Errorf("the password typed again: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if a := s.get("/admin/system"); a.status != http.StatusOK {
			t.Errorf("the administration after the password was typed again: status %d", a.status)
		}

		// Signing in by password needs a password to sign in with.
		if err := s.db.UpdateUserSettings(ctx, s.user("alice").ID, func(settings map[string]json.RawMessage) error {
			delete(settings, "passwordHash")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		s.system(func(system *store.System) { system.AuthType = store.AuthNone })
		if a := s.post("/admin/authentication", url.Values{"auth_type": {"form"}}); a.status != http.StatusBadRequest || !strings.Contains(a.body, "locked out") {
			t.Errorf("turning the login on without a password: status %d", a.status)
		}
		// Without a login there is no password to type again.
		*now = now.Add(time.Hour)
		if a := s.get("/admin/system"); a.status != http.StatusOK {
			t.Errorf("the administration of an installation without logins: status %d", a.status)
		}
	})
}

// R15: the journal shows a user what went wrong for them, and an
// administrator what went wrong for anybody.
func TestJournalPage(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		s.clock()
		for i, l := range []store.Log{
			{User: "alice", Level: "WARN", Message: "feed failed url=http://one.example/ 100%"},
			{User: "bob", Level: "ERROR", Message: "feed failed url=http://two.example/"},
			{User: "alice", Level: "ERROR", Message: "<b>other</b> trouble: Новости"},
		} {
			l.Time = 1791280000 + int64(i)
			if err := s.db.AddLog(ctx, &l); err != nil {
				t.Fatal(err)
			}
		}
		s.login("bob", "bob-web-password", nil)
		body := s.shown("/log")
		if !strings.Contains(body, "http://two.example/") || strings.Contains(body, "one.example") || strings.Contains(body, "other") {
			t.Errorf("the journal of bob:\n%s", body)
		}
		if all := s.page("/log?all=1"); strings.Contains(all, "one.example") {
			t.Error("a plain user who asks for the journal of everybody gets it")
		}
		if a := s.post("/log/clear", url.Values{"all": {"1"}}); a.status != http.StatusForbidden {
			t.Errorf("a plain user clearing the journal of everybody: status %d", a.status)
		}
		s.cookies = nil
		s.asAlice()
		body = s.shown("/log")
		first, second := strings.Index(body, "&lt;b&gt;other&lt;/b&gt; trouble: Новости"), strings.Index(body, "one.example")
		if first < 0 || second < first || strings.Contains(body, "two.example") {
			t.Errorf("the journal of alice, newest first:\n%s", body)
		}
		if all := s.shown("/log?all=1"); !strings.Contains(all, "two.example") || !strings.Contains(all, "<td>bob</td>") {
			t.Errorf("the journal of everybody:\n%s", all)
		}
		for query, want := range map[string]int{"FEED": 1, "100%": 1, "%": 1, "nothing": 0, "Новости": 1, "TROUBLE: Нов": 1} {
			if got := strings.Count(s.page("/log?q="+url.QueryEscape(query)), "<td>WARN</td>") + strings.Count(s.page("/log?q="+url.QueryEscape(query)), "<td>ERROR</td>"); got != want {
				t.Errorf("the journal searched for %q lists %d records, want %d", query, got, want)
			}
		}
		_, body = s.follow("/log/clear", nil)
		if logs, _ := s.db.Logs(ctx, store.LogQuery{}); notice(body) != "Journal cleared." || len(logs) != 1 || logs[0].User != "bob" {
			t.Errorf("after clearing one's own journal: notice %q, %d records left", notice(body), len(logs))
		}
		s.follow("/log/clear", url.Values{"all": {"1"}})
		if logs, _ := s.db.Logs(ctx, store.LogQuery{}); len(logs) != 0 {
			t.Errorf("after clearing the journal of everybody %d records are left", len(logs))
		}
		// A long journal is paged through.
		for i := range logPage + 3 {
			if err := s.db.AddLog(ctx, &store.Log{Time: int64(i), User: "alice", Level: "WARN", Message: fmt.Sprintf("record %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		body = s.page("/log")
		next := nextPage.FindStringSubmatch(body)
		if strings.Count(body, "<td>WARN</td>") != logPage || next == nil {
			t.Fatalf("the first page of a long journal lists %d records", strings.Count(body, "<td>WARN</td>"))
		}
		if rest := s.page(unescape(next[1])); strings.Count(rest, "<td>WARN</td>") != 3 || !strings.Contains(rest, "record 0<") {
			t.Errorf("the second page of a long journal lists %d records", strings.Count(rest, "<td>WARN</td>"))
		}
	})
}
