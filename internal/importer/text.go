package importer

import (
	"regexp"
	"strings"
)

// entityDecoder undoes PHP htmlspecialchars: FreshRSS stores names, titles
// and links with these characters encoded. Other entities are text the feed
// author wrote and stay as they are, the way htmlspecialchars_decode leaves
// them. The replacement is a single pass: "&amp;lt;" becomes "&lt;".
var entityDecoder = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&#039;", "'",
	"&#39;", "'",
	"&#x27;", "'",
	"&apos;", "'",
)

func decodeText(s string) string {
	return entityDecoder.Replace(s)
}

var (
	authorSemicolon = regexp.MustCompile(`\s*;\s*`)
	authorComma     = regexp.MustCompile(`\s*,\s*`)
	tagSeparator    = regexp.MustCompile(`\s*[#,]\s*`)
)

// splitAuthors turns the stored author string into names the way
// FreshRSS_Entry::_authors reads it: ";A; B" separated by semicolons, or an
// older comma-separated form. Entities are decoded before splitting on
// semicolons because they contain one themselves.
func splitAuthors(stored string) []string {
	if strings.Contains(stored, ";") {
		return nonEmpty(authorSemicolon.Split(decodeText(stored), -1))
	}
	return nonEmpty(authorComma.Split(decodeText(stored), -1))
}

// splitTags turns the stored "#a #b c" string into tags the way
// FreshRSS_Entry::_tags reads it: split on # and commas first, decode after.
func splitTags(stored string) []string {
	parts := tagSeparator.Split(stored, -1)
	for i, p := range parts {
		parts[i] = decodeText(p)
	}
	return nonEmpty(parts)
}

func nonEmpty(parts []string) []string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
