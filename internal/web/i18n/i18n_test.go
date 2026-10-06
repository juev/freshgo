package i18n

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
)

func load(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMatch(t *testing.T) {
	b := load(t)
	if got := b.Languages(); !reflect.DeepEqual(got, []string{"en", "ru"}) {
		t.Fatalf("Languages = %v, want en first, then ru", got)
	}
	for _, tt := range []struct {
		preferences []string
		want        string
	}{
		{nil, "en"},
		{[]string{""}, "en"},
		{[]string{"ru"}, "ru"},
		{[]string{"ru-RU,ru;q=0.9,en;q=0.8"}, "ru"},
		{[]string{"en-US"}, "en"},
		// The user's setting comes before what the browser asks for.
		{[]string{"en", "ru-RU,ru;q=0.9"}, "en"},
		{[]string{"", "ru"}, "ru"},
		// Languages freshgo does not speak, the way FreshRSS names them.
		{[]string{"zh-CN"}, "en"},
		{[]string{"pt-BR", "ru"}, "ru"},
		{[]string{"de,ru;q=0.5"}, "ru"},
		{[]string{"not a language, at all;;"}, "en"},
		// Ukrainian readers are not handed Russian for being close.
		{[]string{"uk"}, "en"},
	} {
		if got := b.Match(tt.preferences...).Lang(); got != tt.want {
			t.Errorf("Match(%q) = %s, want %s", tt.preferences, got, tt.want)
		}
	}
}

func TestTexts(t *testing.T) {
	b := load(t)
	en, ru := b.Match("en"), b.Match("ru")
	en.messages["test.plain"], ru.messages["test.plain"] = message{text: "%s has feeds"}, message{text: "У %s есть ленты"}
	en.messages["test.count"] = message{forms: map[string]string{"one": "%d entry in %s", "other": "%d entries in %s"}}
	ru.messages["test.count"] = message{forms: map[string]string{
		"one": "%d статья в %s", "few": "%d статьи в %s", "many": "%d статей в %s", "other": "%d статьи в %s",
	}}
	en.messages["test.english_only"] = message{text: "only here"}

	if got := ru.T("test.plain", "alice"); got != "У alice есть ленты" {
		t.Errorf("T = %q", got)
	}
	for n, want := range map[int]string{
		0: "0 статей в A", 1: "1 статья в A", 2: "2 статьи в A", 5: "5 статей в A", 11: "11 статей в A",
		21: "21 статья в A", 22: "22 статьи в A", 112: "112 статей в A", 1001: "1001 статья в A",
	} {
		if got := ru.N("test.count", n, "A"); got != want {
			t.Errorf("ru N(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{0: "0 entries in A", 1: "1 entry in A", 2: "2 entries in A", 21: "21 entries in A"} {
		if got := en.N("test.count", n, "A"); got != want {
			t.Errorf("en N(%d) = %q, want %q", n, got, want)
		}
	}
	// A text missing in a language is shown in English, a text missing
	// everywhere as its key; so is a text asked for in the wrong way.
	if got := ru.T("test.english_only"); got != "only here" {
		t.Errorf("T of a text only English has = %q", got)
	}
	if got := ru.T("test.missing"); got != "test.missing" {
		t.Errorf("T of a missing text = %q, want the key", got)
	}
	if got := ru.T("test.count"); got != "test.count" {
		t.Errorf("T of a counted text = %q, want the key", got)
	}
	if got := ru.N("test.plain", 1); got != "test.plain" {
		t.Errorf("N of a plain text = %q, want the key", got)
	}
}

var verb = regexp.MustCompile(`%(\[\d+\])?[a-zA-Z]`)

func verbs(text string) []string {
	found := verb.FindAllString(text, -1)
	sort.Strings(found)
	return found
}

// Every language has every text, with the same values put in, and every
// plural form its language tells apart.
func TestLanguagesAreComplete(t *testing.T) {
	b := load(t)
	forms := map[string][]string{"en": {"one", "other"}, "ru": {"few", "many", "one", "other"}}
	en := b.languages[Fallback]
	for _, name := range b.Languages() {
		l := b.languages[name]
		want, known := forms[name]
		if !known {
			t.Errorf("%s: the test does not know the plural forms of the language", name)
		}
		for key, reference := range en.messages {
			m, ok := l.messages[key]
			if !ok {
				t.Errorf("%s: no text for %q", name, key)
				continue
			}
			if (m.forms == nil) != (reference.forms == nil) {
				t.Errorf("%s: %q is counted in one language and plain in the other", name, key)
				continue
			}
			if m.forms == nil {
				if m.text == "" || !reflect.DeepEqual(verbs(m.text), verbs(reference.text)) {
					t.Errorf("%s: %q = %q does not take the values of %q", name, key, m.text, reference.text)
				}
				continue
			}
			var got []string
			for form, text := range m.forms {
				got = append(got, form)
				if !reflect.DeepEqual(verbs(text), verbs(reference.forms["other"])) {
					t.Errorf("%s: %q, form %s = %q does not take the values of %q", name, key, form, text, reference.forms["other"])
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %q has the forms %v, want %v", name, key, got, want)
			}
		}
		for key := range l.messages {
			if _, ok := en.messages[key]; !ok {
				t.Errorf("%s: %q has no English text", name, key)
			}
		}
	}
}
