package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/juev/freshgo/internal/hooks"
)

// Every path goes to the part of the server it belongs to, and the address
// of a feed inside a path survives the trip.
func TestRoutes(t *testing.T) {
	part := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, name+" "+r.RequestURI)
		})
	}
	registry := &hooks.Registry{}
	registry.HandleAPI("Share By Mail", part("extension"))
	server := httptest.NewServer(routes(part("api"), part("icons"), extensions(registry)))
	defer server.Close()

	for path, want := range map[string]struct {
		status int
		body   string
	}{
		"/accounts/ClientLogin":                   {200, "api /accounts/ClientLogin"},
		"/reader/api/0/token":                     {200, "api /reader/api/0/token"},
		"/api/greader.php":                        {200, "api /api/greader.php"},
		"/api/greader.php/reader/api/0/token?x=1": {200, "api /api/greader.php/reader/api/0/token?x=1"},
		"/reader/api/0/stream/contents/feed/http%3A%2F%2Fexample.org%2F%2Ffeed": {200, "api /reader/api/0/stream/contents/feed/http%3A%2F%2Fexample.org%2F%2Ffeed"},
		"/favicon/0123": {200, "icons /favicon/0123"},
		"/api/misc.php/Share%20By%20Mail/send?to=x": {200, "extension /api/misc.php/Share%20By%20Mail/send?to=x"},
		"/api/misc.php?ext=Share+By+Mail":           {200, "extension /api/misc.php?ext=Share+By+Mail"},
		"/api/misc.php/Unknown":                     {404, "Not Found!"},
		"/api/misc.php":                             {400, "Bad Request!"},
		"/api/misc.php/":                            {400, "Bad Request!"},
		"/":                                         {404, "404 page not found\n"},
		"/api/greader.phpx":                         {404, "404 page not found\n"},
		"/api/fever.php":                            {404, "404 page not found\n"},
		"/readers":                                  {404, "404 page not found\n"},
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != want.status || string(body) != want.body {
			t.Errorf("GET %s: status %d, body %q; want %d, %q", path, resp.StatusCode, body, want.status, want.body)
		}
	}
}
