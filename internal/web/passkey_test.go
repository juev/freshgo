package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"

	"github.com/juev/freshgo/internal/store"
)

// passkeyBase is the public address the tests serve passkeys from.
const passkeyBase = "https://freshgo.example"

// authenticator is a software authenticator of passkeys: a P-256 key per
// credential, the "none" attestation, the checks of a user (a fingerprint)
// passed. What a test changes in it, a real authenticator would have to be
// made to do.
type authenticator struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	id     []byte
	handle []byte
	// origin and rpID are what the authenticator thinks it talks to.
	origin, rpID string
	// count is the signature counter, flags the flags of the authenticator
	// data: user present, user verified.
	count uint32
	flags byte
	// discoverable is what it tells the server about the credential.
	discoverable bool
}

const (
	flagPresent  = 0x01
	flagVerified = 0x04
	flagAttested = 0x40
)

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &authenticator{t: t, key: key, id: id, origin: passkeyBase, rpID: "freshgo.example", flags: flagPresent | flagVerified, discoverable: true}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64url %q: %v", s, err)
	}
	return b
}

// clientData is what the browser signs the challenge into.
func (a *authenticator) clientData(kind, challenge string) []byte {
	data, err := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	if err != nil {
		a.t.Fatal(err)
	}
	return data
}

func (a *authenticator) authData(attested bool) []byte {
	hash := sha256.Sum256([]byte(a.rpID))
	flags := a.flags
	if attested {
		flags |= flagAttested
	}
	data := append(hash[:], flags)
	data = binary.BigEndian.AppendUint32(data, a.count)
	if !attested {
		return data
	}
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.id)))
	data = append(data, a.id...)
	point, err := a.key.PublicKey.Bytes()
	if err != nil {
		a.t.Fatal(err)
	}
	x, y := point[1:33], point[33:]
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		a.t.Fatal(err)
	}
	return append(data, cose...)
}

// created answers the options of a registration the way the browser hands
// them to the script's request: the body of the finish.
func (a *authenticator) created(options string) string {
	a.t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(options), &o); err != nil {
		a.t.Fatalf("options %s: %v", options, err)
	}
	a.handle = unb64(a.t, o.PublicKey.User.ID)
	object, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(true)})
	if err != nil {
		a.t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id": b64(a.id), "rawId": b64(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64(object),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{"credProps": map[string]any{"rk": a.discoverable}},
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return string(body)
}

// asserted answers the options of a login.
func (a *authenticator) asserted(options string) string {
	a.t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(options), &o); err != nil {
		a.t.Fatalf("options %s: %v", options, err)
	}
	a.count++
	data := a.authData(false)
	client := a.clientData("webauthn.get", o.PublicKey.Challenge)
	hash := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte{}, data...), hash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	if err != nil {
		a.t.Fatal(err)
	}
	response := map[string]any{"clientDataJSON": b64(client), "authenticatorData": b64(data), "signature": b64(signature)}
	if a.handle != nil {
		response["userHandle"] = b64(a.handle)
	}
	body, err := json.Marshal(map[string]any{"id": b64(a.id), "rawId": b64(a.id), "type": "public-key", "response": response, "clientExtensionResults": map[string]any{}})
	if err != nil {
		a.t.Fatal(err)
	}
	return string(body)
}

// passkeyAt is a site whose server knows the public address passkeys are
// bound to.
func passkeyAt(t *testing.T, test func(t *testing.T, s *site)) {
	t.Helper()
	imported(t, Options{BaseURL: passkeyBase}, test)
}

// addPasskey registers a passkey of the user who is logged in, through the
// two requests of the script, and returns the authenticator.
func (s *site) addPasskey(name string) *authenticator {
	s.t.Helper()
	a := newAuthenticator(s.t)
	begin := s.postJSON(passkeyBeginPath, `{"name":`+jsonString(name)+`}`)
	if begin.status != http.StatusOK {
		s.t.Fatalf("begin: status %d: %s", begin.status, begin.body)
	}
	if finish := s.postJSON(passkeyFinishPath, a.created(begin.body)); finish.status != http.StatusNoContent {
		s.t.Fatalf("finish: status %d: %s", finish.status, finish.body)
	}
	return a
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// passkeys returns the passkeys of a user as they are stored.
func (s *site) passkeys(user string) []*store.Passkey {
	s.t.Helper()
	list, err := s.db.Passkeys(context.Background(), s.user(user).ID)
	if err != nil {
		s.t.Fatal(err)
	}
	return list
}

// A user who has just logged in adds a passkey: the page offers it, the
// server gives the options of a passkey that is found without a name and
// checks the user, the answer is stored with the name, and the next one
// excludes the first.
func TestPasskeyRegistration(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		page := s.get("/settings/profile").body
		for _, want := range []string{`id="passkey-add"`, `data-begin="/settings/profile/passkeys/begin"`, `data-finish="/settings/profile/passkeys/finish"`, "You have no passkeys yet."} {
			if !strings.Contains(page, want) {
				t.Errorf("the profile lacks %q", want)
			}
		}

		begin := s.postJSON(passkeyBeginPath, `{"name":"  Laptop "}`)
		if begin.status != http.StatusOK || begin.header.Get("Content-Type") != "application/json" || begin.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("begin: status %d, %v", begin.status, begin.header)
		}
		var options struct {
			PublicKey struct {
				RP                     struct{ ID string }
				User                   struct{ ID, Name string }
				Challenge              string
				ExcludeCredentials     []struct{ ID string }
				AuthenticatorSelection struct {
					ResidentKey      string
					UserVerification string
				}
			}
		}
		if err := json.Unmarshal([]byte(begin.body), &options); err != nil {
			t.Fatal(err)
		}
		pk := options.PublicKey
		if pk.RP.ID != "freshgo.example" || pk.User.Name != "alice" || pk.AuthenticatorSelection.ResidentKey != "required" ||
			pk.AuthenticatorSelection.UserVerification != "required" || len(unb64(t, pk.Challenge)) < 16 || len(pk.ExcludeCredentials) != 0 {
			t.Errorf("options = %+v", pk)
		}
		// The handle is random and says nothing about the user.
		handle := unb64(t, pk.User.ID)
		if len(handle) != 64 || strings.Contains(string(handle), "alice") {
			t.Errorf("user handle = %q", handle)
		}
		cookie := s.cookies[passkeyCookie]
		if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
			t.Errorf("ceremony cookie = %+v", cookie)
		}

		a := newAuthenticator(t)
		finish := s.postJSON(passkeyFinishPath, a.created(begin.body))
		if finish.status != http.StatusNoContent {
			t.Fatalf("finish: status %d: %s", finish.status, finish.body)
		}
		list := s.passkeys("alice")
		if len(list) != 1 || list[0].Name != "Laptop" || string(list[0].CredentialID) != string(a.id) || list[0].Created == 0 || list[0].Used != 0 {
			t.Fatalf("stored = %+v, want the Laptop passkey", list)
		}
		if s.cookies[passkeyCookie] != nil {
			t.Error("the ceremony cookie stays after the answer")
		}
		after := s.get("/settings/profile")
		if !strings.Contains(after.body, "Passkey added.") || !strings.Contains(after.body, ">Laptop<") && !strings.Contains(after.body, `value="Laptop"`) {
			t.Errorf("the profile after registering: %.600s", after.body)
		}

		// The second passkey has the same handle and leaves the first out.
		second := s.postJSON(passkeyBeginPath, `{}`)
		if err := json.Unmarshal([]byte(second.body), &options); err != nil {
			t.Fatal(err)
		}
		if got := unb64(t, options.PublicKey.User.ID); string(got) != string(handle) {
			t.Error("the second passkey has another user handle")
		}
		if len(options.PublicKey.ExcludeCredentials) != 1 || unb64(t, options.PublicKey.ExcludeCredentials[0].ID) == nil {
			t.Errorf("exclude list = %+v, want the first passkey", options.PublicKey.ExcludeCredentials)
		}
		// The same authenticator cannot be registered twice.
		if again := s.postJSON(passkeyFinishPath, a.created(second.body)); again.status != http.StatusBadRequest && again.status != http.StatusConflict {
			t.Errorf("the same credential registered twice: status %d", again.status)
		}
		if got := s.passkeys("alice"); len(got) != 1 {
			t.Errorf("passkeys after a duplicate = %d, want 1", len(got))
		}
		// With no name, the passkey has the default one.
		b := newAuthenticator(t)
		third := s.postJSON(passkeyBeginPath, `{}`)
		if s.postJSON(passkeyFinishPath, b.created(third.body)).status != http.StatusNoContent {
			t.Fatal("the second passkey was not stored")
		}
		if got := s.passkeys("alice"); len(got) != 2 || got[1].Name != "Passkey" {
			t.Errorf("passkeys = %+v, want a second named Passkey", got)
		}
	})
}

// A user without a web password, as one who came through the provider is,
// registers a passkey after a fresh login.
func TestPasskeyForUserWithoutPassword(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		ctx := context.Background()
		u := &store.User{Name: "carol"}
		if err := s.db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		s.startLogin(u)
		a := s.addPasskey("phone")
		if got := s.passkeys("carol"); len(got) != 1 || string(got[0].CredentialID) != string(a.id) {
			t.Errorf("stored = %+v", got)
		}
	})
}

// startLogin gives the browser of the test a login of the user, as the
// provider or a passkey would.
func (s *site) startLogin(u *store.User) {
	s.t.Helper()
	now := s.h.now()
	token := "test-login-of-" + u.Name
	err := s.db.CreateSession(context.Background(), &store.Session{
		TokenHash: tokenHash(token), UserID: u.ID, Created: now.Unix(), Used: now.Unix(),
		Expires: now.Add(time.Hour).Unix(), Authenticated: now.Unix(),
	})
	if err != nil {
		s.t.Fatal(err)
	}
	s.cookies = map[string]*http.Cookie{sessionCookie: {Name: sessionCookie, Value: token}}
}

// A passkey is a login of its own for good, so an old login does not add
// one: it is sent to type the password again.
func TestPasskeyRegistrationNeedsAFreshLogin(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		now := s.clock()
		s.system(func(sys *store.System) { sys.ReauthTime = 120 })
		s.asAlice()
		begin := s.postJSON(passkeyBeginPath, `{"name":"late"}`)
		*now = now.Add(3 * time.Minute)
		if finish := s.postJSON(passkeyFinishPath, newAuthenticator(t).created(begin.body)); finish.status != http.StatusForbidden || !strings.Contains(finish.body, `"reauth":"/reauth?next=%2Fsettings%2Fprofile"`) {
			t.Errorf("finish with an old login: status %d: %s", finish.status, finish.body)
		}
		if late := s.postJSON(passkeyBeginPath, `{"name":"late"}`); late.status != http.StatusForbidden || !strings.Contains(late.body, "/reauth") {
			t.Errorf("begin with an old login: status %d: %s", late.status, late.body)
		}
		if got := s.passkeys("alice"); len(got) != 0 {
			t.Errorf("stored with an old login: %+v", got)
		}
		// Typing the password again makes it fresh.
		if a := s.post("/reauth", url.Values{"password": {"alice-web-password"}, "next": {"/settings/profile"}}); a.status != http.StatusSeeOther {
			t.Fatalf("reauth: status %d", a.status)
		}
		s.addPasskey("now")
		// Where the installation never asks again, nothing is asked.
		s.system(func(sys *store.System) { sys.ReauthTime = 0 })
		*now = now.Add(2 * time.Hour)
		s.addPasskey("much later")
		if got := s.passkeys("alice"); len(got) != 2 {
			t.Errorf("passkeys = %d, want 2", len(got))
		}
	})
}

// What the server accepts as the answer of an authenticator is the answer
// to the ceremony this browser began, once, for this user, in this login,
// from the origin of the server, with the user verified.
func TestPasskeyRegistrationRefusals(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		begin := func() string {
			t.Helper()
			a := s.postJSON(passkeyBeginPath, `{"name":"x"}`)
			if a.status != http.StatusOK {
				t.Fatalf("begin: %d", a.status)
			}
			return a.body
		}
		refused := func(what string, status int, body string) {
			t.Helper()
			if a := s.postJSON(passkeyFinishPath, body); a.status != status {
				t.Errorf("%s: status %d, want %d", what, a.status, status)
			}
			if got := s.passkeys("alice"); len(got) != 0 {
				t.Errorf("%s: stored %+v", what, got)
			}
		}

		// Nothing was begun, or it was answered already.
		options := begin()
		answer := newAuthenticator(t).created(options)
		if a := s.postJSON(passkeyFinishPath, answer); a.status != http.StatusNoContent {
			t.Fatalf("finish: %d", a.status)
		}
		if err := s.db.DeleteUserPasskeys(context.Background(), s.user("alice").ID); err != nil {
			t.Fatal(err)
		}
		refused("an answer to a ceremony that was answered", http.StatusBadRequest, answer)
		refused("an answer to nothing", http.StatusBadRequest, newAuthenticator(t).created(options))

		// A wrong answer spends the challenge as well.
		wrong := newAuthenticator(t)
		wrong.origin = "https://evil.example"
		refused("another origin", http.StatusBadRequest, wrong.created(begin()))
		refused("the right answer after a wrong one", http.StatusBadRequest, newAuthenticator(t).created(options))

		noVerify := newAuthenticator(t)
		noVerify.flags = flagPresent
		refused("a user who was not verified", http.StatusBadRequest, noVerify.created(begin()))

		wrongRP := newAuthenticator(t)
		wrongRP.rpID = "evil.example"
		refused("another relying party", http.StatusBadRequest, wrongRP.created(begin()))

		notDiscoverable := newAuthenticator(t)
		notDiscoverable.discoverable = false
		refused("a credential that is not discoverable", http.StatusBadRequest, notDiscoverable.created(begin()))

		// The challenge of an earlier ceremony is not this one's.
		earlier := begin()
		begin()
		refused("the challenge of an earlier ceremony", http.StatusBadRequest, newAuthenticator(t).created(earlier))

		refused("garbage", http.StatusBadRequest, "{")
		refused("a body that is too large", http.StatusBadRequest, `{"id":"`+strings.Repeat("a", maxPasskeyBody)+`"}`)
	})
}

// A ceremony belongs to the browser, the user and the login that began it:
// the secret of one browser does not finish the ceremony of another.
func TestPasskeyCeremonyBelongsToItsBrowser(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		bob := &site{t: t, db: s.db, h: s.h}
		if a := bob.login("bob", "bob-web-password", nil); a.status != http.StatusSeeOther {
			t.Fatalf("login of bob: %d", a.status)
		}
		second := &site{t: t, db: s.db, h: s.h}
		second.asAlice()

		for name, thief := range map[string]*site{"another user": bob, "another login of the same user": second} {
			options := s.postJSON(passkeyBeginPath, `{"name":"alice's"}`).body
			thief.cookies[passkeyCookie] = s.cookies[passkeyCookie]
			if a := thief.postJSON(passkeyFinishPath, newAuthenticator(t).created(options)); a.status != http.StatusBadRequest {
				t.Errorf("%s finishes the ceremony: status %d", name, a.status)
			}
			if len(s.passkeys("alice")) != 0 || len(bob.passkeys("bob")) != 0 {
				t.Errorf("%s: a passkey was stored for the wrong ceremony", name)
			}
			// The ceremony is spent with the attempt.
			if a := s.postJSON(passkeyFinishPath, newAuthenticator(t).created(options)); a.status != http.StatusBadRequest {
				t.Errorf("%s: the owner finishes a ceremony that was taken: status %d", name, a.status)
			}
		}

		// A visitor has no ceremony to begin.
		visitor := &site{t: t, db: s.db, h: s.h}
		if a := visitor.postJSON(passkeyBeginPath, `{}`); a.status != http.StatusForbidden {
			t.Errorf("a visitor began a registration: status %d, want 403", a.status)
		}
	})
}

// A ceremony that waited too long is gone.
func TestPasskeyCeremonyExpires(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		now := s.clock()
		s.asAlice()
		options := s.postJSON(passkeyBeginPath, `{"name":"slow"}`).body
		*now = now.Add(passkeyLife + time.Second)
		if a := s.postJSON(passkeyFinishPath, newAuthenticator(t).created(options)); a.status != http.StatusBadRequest {
			t.Errorf("an answer after %v: status %d, want 400", passkeyLife, a.status)
		}
		if len(s.passkeys("alice")) != 0 {
			t.Error("a passkey was stored from an expired ceremony")
		}
	})
}

// Where the server knows no address passkeys can be bound to, they are
// refused clearly and the profile says why.
func TestPasskeyNeedsAnOrigin(t *testing.T) {
	for name, base := range map[string]string{
		"no address":      "",
		"plain http":      "http://reader.example",
		"an IP address":   "https://192.0.2.7",
		"an IPv6 address": "https://[2001:db8::1]:8443",
	} {
		t.Run(name, func(t *testing.T) {
			imported(t, Options{BaseURL: base}, func(t *testing.T, s *site) {
				s.asAlice()
				page := s.get("/settings/profile").body
				if strings.Contains(page, `id="passkey-add"`) || !strings.Contains(page, "public address over HTTPS") {
					t.Errorf("the profile: %.300s", page)
				}
				if a := s.postJSON(passkeyBeginPath, `{}`); a.status != http.StatusNotFound {
					t.Errorf("begin: status %d, want 404", a.status)
				}
			})
		})
	}
	// Browsers treat localhost as secure, so it is a place for passkeys.
	imported(t, Options{BaseURL: "http://localhost:8080"}, func(t *testing.T, s *site) {
		s.asAlice()
		if a := s.postJSON(passkeyBeginPath, `{}`); a.status != http.StatusOK {
			t.Errorf("begin on localhost: status %d, want 200", a.status)
		}
	})
}

func TestPasskeyOrigin(t *testing.T) {
	for base, want := range map[string]string{
		"https://Freshgo.Example":           "https://freshgo.example freshgo.example",
		"https://freshgo.example:8443/rss/": "https://freshgo.example:8443 freshgo.example",
		"http://localhost:8080":             "http://localhost:8080 localhost",
		"http://freshgo.example":            "",
		"https://127.0.0.1":                 "",
		"":                                  "",
		"freshgo.example":                   "",
	} {
		origin, rpID, ok := passkeyOrigin(base)
		got := ""
		if ok {
			got = origin + " " + rpID
		}
		if got != want {
			t.Errorf("passkeyOrigin(%q) = %q, want %q", base, got, want)
		}
	}
}

// Passkeys are listed, renamed and removed by their user alone, and go with
// the account.
func TestPasskeyManagement(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		s.addPasskey("laptop")
		s.addPasskey("phone")
		list := s.passkeys("alice")
		page := s.get("/settings/profile").body
		for _, p := range list {
			if !strings.Contains(page, `action="/settings/profile/passkeys/`+itoa(p.ID)+`/rename"`) || !strings.Contains(page, `action="/settings/profile/passkeys/`+itoa(p.ID)+`/delete"`) {
				t.Errorf("no forms for passkey %d", p.ID)
			}
		}
		if !strings.Contains(page, "never") {
			t.Error("a passkey that never logged in is not said to be unused")
		}
		// The removal asks first, as the other removals do (#217).
		for _, want := range []string{"<summary>Delete…</summary>", "The passkey is deleted. The device that uses it can no longer log in with it."} {
			if !strings.Contains(page, want) {
				t.Errorf("the profile page lacks %q", want)
			}
		}

		target := "/settings/profile/passkeys/" + itoa(list[0].ID)
		location, body := s.follow(target+"/rename", url.Values{"name": {" desk "}})
		if location != "/settings/profile" || notice(body) != "Saved." || s.passkeys("alice")[0].Name != "desk" {
			t.Errorf("renamed: at %q, notice %q, name %q", location, notice(body), s.passkeys("alice")[0].Name)
		}
		for name, bad := range map[string]string{"empty": " ", "long": strings.Repeat("a", 65), "control": "a\x07b"} {
			if a := s.post(target+"/rename", url.Values{"name": {bad}}); a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) || s.passkeys("alice")[0].Name != "desk" {
				t.Errorf("a %s name: status %d", name, a.status)
			}
		}

		// Bob cannot touch what is alice's.
		bob := &site{t: t, db: s.db, h: s.h}
		bob.login("bob", "bob-web-password", nil)
		for _, action := range []string{"rename", "delete"} {
			if a := bob.post(target+"/"+action, url.Values{"name": {"mine"}}); a.status != http.StatusNotFound {
				t.Errorf("bob's %s of alice's passkey: status %d, want 404", action, a.status)
			}
		}
		if got := s.passkeys("alice"); len(got) != 2 || got[0].Name != "desk" {
			t.Errorf("alice's passkeys after bob's attempts: %+v", got)
		}
		if a := s.post("/settings/profile/passkeys/x/delete", url.Values{}); a.status != http.StatusNotFound {
			t.Errorf("a passkey that is no number: status %d", a.status)
		}

		location, body = s.follow(target+"/delete", url.Values{})
		if location != "/settings/profile" || notice(body) != "Passkey removed." {
			t.Errorf("removed: at %q, notice %q", location, notice(body))
		}
		if got := s.passkeys("alice"); len(got) != 1 || got[0].Name != "phone" {
			t.Errorf("passkeys after the removal: %+v", got)
		}
		if a := s.post(target+"/delete", url.Values{}); a.status != http.StatusNotFound {
			t.Errorf("removing it twice: status %d, want 404", a.status)
		}

		// They go with the account.
		theirs := bob.addPasskey("bob's")
		if a := bob.post("/settings/profile/delete", url.Values{"password": {"bob-web-password"}}); a.status != http.StatusSeeOther {
			t.Fatalf("deleting bob: status %d", a.status)
		}
		if _, err := s.db.PasskeyByCredential(context.Background(), theirs.id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the passkey of a deleted user: %v, want ErrNotFound", err)
		}
		if len(s.passkeys("alice")) != 1 {
			t.Error("alice lost a passkey with bob's account")
		}
	})
}
