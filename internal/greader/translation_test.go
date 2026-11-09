package greader

import (
	"encoding/json"
	"github.com/juev/freshgo/internal/translate"
	"net/http"
	"testing"
)

func TestTranslationConfiguredCapability(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.handler.translator = translate.New(w.db, nil)
		var got struct {
			Version int
			Enabled bool
		}
		w.json(Alias+"/api/freshgo/translation", &got)
		if got.Version != 1 || !got.Enabled {
			t.Fatalf("configured capability = %+v, want enabled", got)
		}
	})
}

func TestTranslationCapability(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		status, body := w.get(w.auth, "/api/freshgo/translation")
		if status != http.StatusOK {
			t.Fatalf("capability status = %d (%s), want 200", status, body)
		}
		var got struct {
			Version int  `json:"version"`
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if got.Version != 1 || got.Enabled {
			t.Fatalf("unconfigured capability = %+v, want version 1 disabled", got)
		}
		for _, auth := range []string{"", "GoogleLogin auth=alice/wrong"} {
			if code, _ := w.get(auth, "/api/freshgo/translation"); code != 401 {
				t.Errorf("unauthenticated capability = %d", code)
			}
		}
	})
}
