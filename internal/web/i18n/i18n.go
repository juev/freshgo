// Package i18n gives the web interface its texts in the language of the
// reader.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

//go:embed locales/*.json
var locales embed.FS

// Fallback is the language of a text that is missing in the language asked
// for, and of readers whose languages freshgo does not speak.
const Fallback = "en"

// message is a text, or the forms of a text that depends on a number: the
// CLDR plural categories "zero", "one", "two", "few", "many" and "other".
type message struct {
	text  string
	forms map[string]string
}

func (m *message) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &m.text); err == nil {
		return nil
	}
	return json.Unmarshal(data, &m.forms)
}

// Bundle holds the texts of every language.
type Bundle struct {
	languages map[string]*Localizer
	matcher   language.Matcher
	// names are the languages in the order the matcher knows them.
	names []string
}

// Load reads the languages built into the binary.
func Load() (*Bundle, error) {
	files, err := locales.ReadDir("locales")
	if err != nil {
		return nil, err
	}
	b := &Bundle{languages: map[string]*Localizer{}}
	for _, f := range files {
		name := strings.TrimSuffix(f.Name(), ".json")
		data, err := locales.ReadFile(path.Join("locales", f.Name()))
		if err != nil {
			return nil, err
		}
		tag, err := language.Parse(name)
		if err != nil {
			return nil, fmt.Errorf("i18n: %s: %w", f.Name(), err)
		}
		l := &Localizer{name: name, tag: tag}
		if err := json.Unmarshal(data, &l.messages); err != nil {
			return nil, fmt.Errorf("i18n: %s: %w", f.Name(), err)
		}
		b.languages[name] = l
		b.names = append(b.names, name)
	}
	fallback := b.languages[Fallback]
	if fallback == nil {
		return nil, fmt.Errorf("i18n: no %s.json", Fallback)
	}
	// The matcher falls back to its first language.
	sort.Slice(b.names, func(i, j int) bool {
		return b.names[i] == Fallback || b.names[j] != Fallback && b.names[i] < b.names[j]
	})
	tags := make([]language.Tag, len(b.names))
	for i, name := range b.names {
		tags[i] = b.languages[name].tag
		b.languages[name].fallback = fallback
	}
	b.matcher = language.NewMatcher(tags)
	return b, nil
}

// Languages lists the languages of the interface, the fallback first.
func (b *Bundle) Languages() []string {
	return append([]string(nil), b.names...)
}

// Match returns the texts in the best language for a reader. Preferences
// come most important first; each is a language tag or the value of an
// Accept-Language header. Empty and malformed ones are passed over.
func (b *Bundle) Match(preferences ...string) *Localizer {
	var wanted []language.Tag
	for _, p := range preferences {
		tags, _, err := language.ParseAcceptLanguage(p)
		if err != nil {
			continue
		}
		wanted = append(wanted, tags...)
	}
	// A language merely related to one of ours (Ukrainian to Russian) is
	// not what the reader asked for.
	_, index, confidence := b.matcher.Match(wanted...)
	if confidence < language.High {
		index = 0
	}
	return b.languages[b.names[index]]
}

// Localizer has the texts of one language.
type Localizer struct {
	name     string
	tag      language.Tag
	messages map[string]message
	fallback *Localizer
}

// Lang is the language as an HTML lang attribute takes it.
func (l *Localizer) Lang() string { return l.name }

func (l *Localizer) lookup(key string) (message, *Localizer, bool) {
	if m, ok := l.messages[key]; ok {
		return m, l, true
	}
	if m, ok := l.fallback.messages[key]; ok {
		return m, l.fallback, true
	}
	return message{}, nil, false
}

// T returns the text of key with args put in as fmt.Sprintf does. A key
// without a text comes back as it is, which shows in the page and in tests.
func (l *Localizer) T(key string, args ...any) string {
	m, _, ok := l.lookup(key)
	if !ok || m.forms != nil {
		return key
	}
	if len(args) == 0 {
		return m.text
	}
	return fmt.Sprintf(m.text, args...)
}

// N returns the text of key in the form the language uses for n things; n is
// the first value put into the text, args follow.
func (l *Localizer) N(key string, n int, args ...any) string {
	m, from, ok := l.lookup(key)
	if !ok || m.forms == nil {
		return key
	}
	text, ok := m.forms[category(from.tag, n)]
	if !ok {
		text = m.forms["other"]
	}
	return fmt.Sprintf(text, append([]any{n}, args...)...)
}

// category names the CLDR plural category of n in a language.
func category(tag language.Tag, n int) string {
	if n < 0 {
		n = -n
	}
	digits := []byte(fmt.Sprint(n))
	for i := range digits {
		digits[i] -= '0'
	}
	switch plural.Cardinal.MatchDigits(tag, digits, len(digits), 0) {
	case plural.Zero:
		return "zero"
	case plural.One:
		return "one"
	case plural.Two:
		return "two"
	case plural.Few:
		return "few"
	case plural.Many:
		return "many"
	default:
		return "other"
	}
}
