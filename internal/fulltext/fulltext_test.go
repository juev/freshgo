package fulltext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
)

const oracleDir = "../../testdata/reference/oracle"

// oracleHost is where the pages lived when FreshRSS read them.
const oracleHost = "http://127.0.0.1:8080"

func newClient(t *testing.T, server *httptest.Server) *fetch.Client {
	t.Helper()
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(oracleDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// R12: the selectors pick, and the result is cleaned, as in FreshRSS.
func TestAgainstFreshRSS(t *testing.T) {
	var cases []struct {
		Name     string `json:"name"`
		Page     string `json:"page"`
		Selector string `json:"selector"`
		Filter   string `json:"filter"`
	}
	readJSON(t, "fulltext-cases.json", &cases)
	var want map[string]string
	readJSON(t, "fulltext.json", &want)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(oracleDir, "articles"))))
	t.Cleanup(server.Close)
	client := newClient(t, server)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			expected, ok := want[c.Name]
			if !ok {
				t.Fatal("no result of FreshRSS for the case; run testdata/reference/oracle/generate.sh")
			}
			expected = strings.ReplaceAll(expected, oracleHost, server.URL)
			// When the filter removes something from the cleaned markup too,
			// FreshRSS returns the body element around the content.
			if inner, wrapped := strings.CutPrefix(expected, "<body>"); wrapped {
				if c.Name != "filter by attribute" {
					t.Errorf("FreshRSS wrapped the content in a body element: %q", expected)
				}
				expected = strings.TrimSuffix(inner, "</body>")
			}
			got, err := Article(context.Background(), client, Request{
				URL: server.URL + "/" + c.Page, Selector: c.Selector, Filter: c.Filter,
			})
			if errors.Is(err, ErrNoElements) && expected == "" {
				return
			}
			if err != nil {
				t.Fatalf("Article: %v", err)
			}
			if got != expected {
				t.Errorf("content\n got %q\nwant %q", got, expected)
			}
		})
	}
}
