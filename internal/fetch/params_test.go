package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestFeedParams(t *testing.T) {
	proxy := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	tests := []struct {
		name       string
		httpAuth   string
		attributes string
		want       Params
	}{
		{"nothing", "", ``, Params{}},
		{"empty object", "", `{}`, Params{}},
		{"credentials", "alice:se:cret", `{}`, Params{BasicAuth: "alice:se:cret"}},
		{"unrelated attributes", "", `{"xpath":{"item":"//li"},"read_upon_gone":true}`, Params{}},
		{"empty curl_params as PHP writes them", "", `{"curl_params":[]}`, Params{}},
		{"null curl_params", "", `{"curl_params":null,"ssl_verify":null,"timeout":null}`, Params{}},

		{"certificate check off", "", `{"ssl_verify":false}`, Params{Insecure: true}},
		{"certificate check off as a number", "", `{"ssl_verify":0}`, Params{Insecure: true}},
		{"certificate check on", "", `{"ssl_verify":true}`, Params{}},
		{"timeout", "", `{"timeout":45}`, Params{Timeout: 45 * time.Second}},
		{"timeout as a string", "", `{"timeout":"45"}`, Params{Timeout: 45 * time.Second}},
		{"timeout of zero", "", `{"timeout":0}`, Params{}},

		{"user agent and cookie", "", `{"curl_params":{"10018":"Mozilla/5.0","10022":"a=1; b=2"}}`,
			Params{UserAgent: "Mozilla/5.0", Cookie: "a=1; b=2"}},
		{"cookie engine", "", `{"curl_params":{"10031":""}}`, Params{CookieEngine: true}},
		{"headers", "", `{"curl_params":{"10023":["X-Key: abc","Accept:text/plain","X-Key: def","no colon",": empty name"]}}`,
			Params{Header: http.Header{"X-Key": {"abc", "def"}, "Accept": {"text/plain"}}}},
		{"headers as an object", "", `{"curl_params":{"10023":{"0":"A: 1","2":"B: 2","10":"A: 3"}}}`,
			Params{Header: http.Header{"A": {"1", "3"}, "B": {"2"}}}},
		{"authentication headers are dropped", "", `{"curl_params":{"10023":["Remote-User: root","X_WebAuth_User : root","remote user: root","X-Remote-User-Agent: x"]}}`,
			Params{Header: http.Header{"X-Remote-User-Agent": {"x"}}}},
		{"header names that cannot be sent", "", `{"curl_params":{"10023":["X Foo: 1","X-Ok: 2","Bad\tName: 3"]}}`,
			Params{Header: http.Header{"X-Ok": {"2"}}}},
		{"only dropped headers", "", `{"curl_params":{"10023":["Remote-User: root"]}}`, Params{}},

		{"post", "", `{"curl_params":{"47":true,"10015":"{\"q\":1}"}}`, Params{Post: true, PostBody: `{"q":1}`}},
		{"post without a body", "", `{"curl_params":{"47":1}}`, Params{Post: true}},
		{"post switched off keeps no body", "", `{"curl_params":{"47":false,"10015":"a=1"}}`, Params{}},

		{"redirect limit", "", `{"curl_params":{"68":7,"52":true}}`, Params{MaxRedirects: 7}},
		{"unlimited redirects", "", `{"curl_params":{"68":-1,"52":true}}`, Params{MaxRedirects: -1}},
		{"no redirects by limit", "", `{"curl_params":{"68":0}}`, Params{NoRedirects: true}},
		{"no redirects by switch", "", `{"curl_params":{"52":false,"68":7}}`, Params{MaxRedirects: 7, NoRedirects: true}},

		{"proxy of the default type", "", `{"curl_params":{"10004":"proxy.example:3128"}}`,
			Params{Proxy: proxy("http://proxy.example:3128")}},
		{"http proxy with credentials", "", `{"curl_params":{"101":0,"10004":"user:pass@proxy.example:3128"}}`,
			Params{Proxy: proxy("http://user:pass@proxy.example:3128")}},
		{"proxy without a port", "", `{"curl_params":{"10004":"proxy.example"}}`,
			Params{Proxy: proxy("http://proxy.example:1080")}},
		{"https proxy without a port", "", `{"curl_params":{"101":2,"10004":"proxy.example"}}`,
			Params{Proxy: proxy("https://proxy.example:443")}},
		{"IPv6 proxy without a port", "", `{"curl_params":{"101":5,"10004":"[fd00::1]"}}`,
			Params{Proxy: proxy("socks5://[fd00::1]:1080")}},
		{"https proxy", "", `{"curl_params":{"101":2,"10004":"proxy.example:443"}}`,
			Params{Proxy: proxy("https://proxy.example:443")}},
		{"socks5 proxy", "", `{"curl_params":{"101":5,"10004":"127.0.0.1:1080"}}`,
			Params{Proxy: proxy("socks5://127.0.0.1:1080")}},
		{"socks5h proxy", "", `{"curl_params":{"101":7,"10004":"127.0.0.1:1080"}}`,
			Params{Proxy: proxy("socks5h://127.0.0.1:1080")}},
		{"the type wins over a scheme in the address", "", `{"curl_params":{"101":5,"10004":"http://127.0.0.1:1080"}}`,
			Params{Proxy: proxy("socks5://127.0.0.1:1080")}},
		{"proxy type none", "", `{"curl_params":{"101":-1,"10004":"proxy.example:3128"}}`, Params{}},
		{"proxy type none, legacy", "", `{"curl_params":{"101":3,"10004":"proxy.example:3128"}}`, Params{}},
		{"proxy type without an address", "", `{"curl_params":{"101":5}}`, Params{}},

		{"options FreshRSS does not allow are ignored", "", `{"curl_params":{"64":false,"81":0,"10005":"u:p"}}`, Params{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FeedParams(tt.httpAuth, json.RawMessage(tt.attributes))
			if err != nil {
				t.Fatalf("FeedParams: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FeedParams:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestFeedParamsErrors(t *testing.T) {
	if _, err := FeedParams("", json.RawMessage(`{"curl_params":`)); err == nil {
		t.Error("malformed attributes were accepted")
	}
	for name, attributes := range map[string]string{
		"socks4":       `{"curl_params":{"101":4,"10004":"127.0.0.1:1080"}}`,
		"socks4a":      `{"curl_params":{"101":6,"10004":"127.0.0.1:1080"}}`,
		"unknown type": `{"curl_params":{"101":42,"10004":"127.0.0.1:1080"}}`,
	} {
		p, err := FeedParams("", json.RawMessage(attributes))
		if err == nil {
			// The type is known to FreshRSS; the client is the one to refuse it.
			c := newClient(t, Options{})
			_, err = c.Fetch(context.Background(), Request{URL: "http://example.org/", Params: p})
		}
		if !errors.Is(err, ErrUnsupportedProxy) {
			t.Errorf("%s: %v, want ErrUnsupportedProxy", name, err)
		}
	}
}

// The settings read from a feed reach the wire.
func TestFeedParamsApplied(t *testing.T) {
	var got *http.Request
	s := serve(t, func(_ http.ResponseWriter, r *http.Request) { got = r.Clone(context.Background()) })
	c := newClient(t, Options{}, s)

	attributes := `{"curl_params":{"10018":"Feed Agent","10022":"session=1","10023":["X-Key: abc","Accept: text/plain","Accept-Encoding: br"]}}`
	p, err := FeedParams("alice:secret", json.RawMessage(attributes))
	if err != nil {
		t.Fatalf("FeedParams: %v", err)
	}
	if _, err := c.Fetch(context.Background(), Request{URL: s.URL, Accept: AcceptFeed, Params: p}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if user, password, ok := got.BasicAuth(); !ok || user != "alice" || password != "secret" {
		t.Errorf("credentials %q:%q, sent %v", user, password, ok)
	}
	for name, want := range map[string]string{
		"User-Agent": "Feed Agent",
		"Cookie":     "session=1",
		"X-Key":      "abc",
		"Accept":     "text/plain",
		// A compression the client cannot unpack is not asked for.
		"Accept-Encoding": "gzip",
	} {
		if v := got.Header.Get(name); v != want {
			t.Errorf("%s: %q, want %q", name, v, want)
		}
	}
}
