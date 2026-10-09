package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	for _, address := range []string{"ws://browser:9222", "wss://browsers.example/?token=t"} {
		if _, err := New(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"", "http://browser:9222", "browser:9222", "ws://", "ws://%zz"} {
		if _, err := New(address); err == nil {
			t.Errorf("%q is taken for the address of a browser", address)
		}
	}
}

// A browser named without a path is asked for its socket, and is reached
// at the address it was named by, whatever it calls itself.
func TestSocket(t *testing.T) {
	status, host := http.StatusOK, ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		host = r.Host
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"Browser":"Chrome/1","webSocketDebuggerUrl":"ws://0.0.0.0:9222/devtools/browser/abc?x=1"}`))
	}))
	t.Cleanup(server.Close)
	at := strings.TrimPrefix(server.URL, "http://")
	_, port, _ := strings.Cut(at, ":")
	socket := func(address string) (string, error) {
		t.Helper()
		b, err := New(address)
		if err != nil {
			t.Fatal(err)
		}
		return b.socket(context.Background())
	}

	if got, err := socket("ws://" + at); err != nil || got != "ws://"+at+"/devtools/browser/abc?x=1" {
		t.Errorf("the socket of the browser = %q, %v", got, err)
	}
	// Chrome answers only a request that names it by its address.
	if got, err := socket("ws://localhost:" + port + "/"); err != nil || !strings.HasSuffix(got, ":"+port+"/devtools/browser/abc?x=1") || strings.Contains(got, "localhost") || strings.Contains(host, "localhost") {
		t.Errorf("a browser named by name: socket %q, asked as %q, %v", got, host, err)
	}
	// An address with a path is the socket itself: nothing is asked.
	host = ""
	for _, whole := range []string{"ws://" + at + "/devtools/browser/own", "wss://browsers.example/?token=t"} {
		if got, err := socket(whole); err != nil || got != whole || host != "" {
			t.Errorf("%s: socket %q, asked as %q, %v", whole, got, host, err)
		}
	}
	status = http.StatusInternalServerError
	if got, err := socket("ws://" + at); err == nil {
		t.Errorf("a browser that does not say its socket: %q", got)
	}
}
