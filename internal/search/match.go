package search

import (
	"strconv"
	"strings"
)

// Entry is what a query looks at in an entry. Title, authors, link and tags
// are plain text; Content is HTML.
type Entry struct {
	ID         int64
	FeedID     int64
	CategoryID int64
	Title      string
	Authors    []string
	Content    string
	Link       string
	// Tags are the categories the feed gave the entry.
	Tags             []string
	Published        int64
	LastModified     int64
	LastUserModified int64
	// Labels are the labels the user put on the entry. Only a query read
	// with Options.Labels looks at them.
	Labels []Label

	// Forms of the texts that matching needs, computed when first asked
	// for: the entry must not change after it was first matched.
	id            string
	title         *string
	content       *string
	authors       *string
	authorsByLine *string
	tags          []string
}

// Label is a label of the user.
type Label struct {
	ID   int64
	Name string
}

// fold makes text comparable without regard to case. Accents stay: FreshRSS
// compares the way its database does, and freshgo takes the rule of
// PostgreSQL, ILIKE, for both of its engines.
func fold(s string) string {
	return strings.ToLower(s)
}

// inHTML is the form a plain string has inside HTML text.
var inHTML = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func lazy(cache **string, text func() string) string {
	if *cache == nil {
		s := text()
		*cache = &s
	}
	return **cache
}

func (s *Entry) foldedTitle() string {
	return lazy(&s.title, func() string { return fold(s.Title) })
}

func (s *Entry) foldedContent() string {
	return lazy(&s.content, func() string { return fold(s.Content) })
}

func (s *Entry) foldedAuthors() string {
	return lazy(&s.authors, func() string { return fold(strings.Join(s.Authors, ";")) })
}

func (s *Entry) linedAuthors() string {
	return lazy(&s.authorsByLine, func() string { return strings.Join(s.Authors, "\n") })
}

func (s *Entry) foldedTags() []string {
	if s.tags == nil {
		s.tags = make([]string, len(s.Tags))
		for i, tag := range s.Tags {
			s.tags[i] = fold(strings.TrimLeft(tag, "#"))
		}
	}
	return s.tags
}

// Match reports whether the entry meets the query.
func (q *Query) Match(e *Entry) bool {
	if e.id == "" {
		e.id = strconv.FormatInt(e.ID, 10)
	}
	return q.match(e)
}

func (q *Query) match(s *Entry) bool {
	ok := true
	for _, part := range q.parts {
		switch part.op {
		case opOr:
			ok = ok || part.match(s)
		case opAndNot:
			ok = ok && !part.match(s)
		case opOrNot:
			ok = ok || !part.match(s)
		default:
			ok = ok && part.match(s)
		}
	}
	for _, t := range q.terms {
		if t.match(s) {
			return true
		}
		ok = false
	}
	return ok
}

// within tells whether a value is inside the bounds of the span that are set.
func (sp span) within(v, scale int64) bool {
	return (sp.min == 0 || v >= sp.min*scale) && (sp.max == 0 || v <= sp.max*scale)
}

// outside is the negation as FreshRSS matches it: the value has to be before
// the lower bound and after the upper one, whichever are set. With both set
// nothing passes.
func (sp span) outside(v, scale int64) bool {
	return (sp.min == 0 || v < sp.min*scale) && (sp.max == 0 || v > sp.max*scale)
}

func (t *term) match(s *Entry) bool {
	const micro = 1_000_000
	switch {
	case t.entryIDs != nil && !containsString(t.entryIDs, s.id),
		t.notEntryIDs != nil && containsString(t.notEntryIDs, s.id),
		!t.date.within(s.ID, micro), !t.notDate.outside(s.ID, micro),
		!t.pubdate.within(s.Published, 1), !t.notPubdate.outside(s.Published, 1),
		!t.modified.within(s.LastModified, 1), !t.notModified.outside(s.LastModified, 1),
		!t.userdate.within(s.LastUserModified, 1), !t.notUserdate.outside(s.LastUserModified, 1),
		t.feedIDs != nil && !containsID(t.feedIDs, s.FeedID),
		t.notFeedIDs != nil && containsID(t.notFeedIDs, s.FeedID),
		t.categoryIDs != nil && !containsID(t.categoryIDs, s.CategoryID),
		t.notCategoryIDs != nil && containsID(t.notCategoryIDs, s.CategoryID):
		return false
	}
	if t.byLabel && !t.matchLabels(s) {
		return false
	}

	// Each demand is a function of one needle; all needles of a list have to
	// pass for a positive demand, none for a negated one.
	contains := func(haystack func() string) func(string) bool {
		return func(needle string) bool { return strings.Contains(haystack(), fold(needle)) }
	}
	matches := func(text string) func(*pattern) bool {
		return func(p *pattern) bool { return p.re.MatchString(text) }
	}
	inContent := func(needle string) bool { return strings.Contains(s.foldedContent(), fold(inHTML.Replace(needle))) }
	inLink := func(needle string) bool {
		return strings.Contains(asciiLower(s.Link), asciiLower(needle))
	}
	hasTag := func(needle string) bool {
		needle = fold(needle)
		for _, tag := range s.foldedTags() {
			if tag == needle {
				return true
			}
		}
		return false
	}
	tagMatches := func(p *pattern) bool {
		for _, tag := range s.Tags {
			if p.re.MatchString(strings.TrimLeft(tag, "#")) {
				return true
			}
		}
		return false
	}
	inTitle := contains(s.foldedTitle)
	anywhere := func(needle string) bool { return inTitle(needle) || inContent(needle) }
	anywhereMatches := func(p *pattern) bool { return p.re.MatchString(s.Title) || p.re.MatchString(s.Content) }

	return demand(t.author, t.notAuthor, contains(s.foldedAuthors), matches(s.linedAuthors())) &&
		demand(t.intitle, t.notIntitle, inTitle, matches(s.Title)) &&
		demand(t.intext, t.notIntext, inContent, matches(s.Content)) &&
		demand(t.tags, t.notTags, hasTag, tagMatches) &&
		demand(t.inurl, t.notInurl, inLink, matches(s.Link)) &&
		demand(t.search, t.notSearch, anywhere, anywhereMatches)
}

// matchLabels checks the demands on the labels the way FreshRSS searches its
// database: the entry needs one label of every list asked for, and none of
// any list ruled out.
func (t *term) matchLabels(s *Entry) bool {
	for _, list := range t.labelIDs {
		if !hasLabel(s, list) {
			return false
		}
	}
	for _, list := range t.notLabelIDs {
		if hasLabel(s, list) {
			return false
		}
	}
	for _, names := range t.labelNames {
		if !hasLabelNamed(s, names) {
			return false
		}
	}
	for _, names := range t.notLabelNames {
		if hasLabelNamed(s, names) {
			return false
		}
	}
	return true
}

// hasLabel tells whether the entry has one of the labels of a list, which is
// []int64 or "*" for any label.
func hasLabel(s *Entry, list any) bool {
	ids, ok := list.([]int64)
	if !ok {
		return len(s.Labels) > 0
	}
	for _, l := range s.Labels {
		if containsID(ids, l.ID) {
			return true
		}
	}
	return false
}

func hasLabelNamed(s *Entry, names []string) bool {
	for _, l := range s.Labels {
		if containsString(names, l.Name) {
			return true
		}
	}
	return false
}

// demand checks the positive and the negated demands on one text.
func demand(wanted, unwanted texts, has func(string) bool, matches func(*pattern) bool) bool {
	for _, needle := range wanted.plain {
		if !has(needle) {
			return false
		}
	}
	for _, p := range wanted.regex {
		if !matches(p) {
			return false
		}
	}
	for _, needle := range unwanted.plain {
		if has(needle) {
			return false
		}
	}
	for _, p := range unwanted.regex {
		if matches(p) {
			return false
		}
	}
	return true
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func containsID(list []int64, v int64) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
