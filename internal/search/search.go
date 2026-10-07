// Package search reads the search language of FreshRSS and tells whether an
// entry matches a query. It is what filter actions are written in.
//
// The language is whatever FreshRSS_BooleanSearch and FreshRSS_Search of
// FreshRSS at commit 219eaf58 make of a string, and matching follows
// FreshRSS_Entry::matches. The parser is a port, quirks included: results
// are compared with FreshRSS itself in the tests. See docs/specs/search.md.
package search

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits of FreshRSS: max_search_length and max_search_parentheses_depth.
const (
	maxLength = 16384
	maxDepth  = 32
)

var (
	// ErrTooLong is returned for a query longer than FreshRSS accepts.
	ErrTooLong = errors.New("search: query is too long")
	// ErrTooDeep is returned for parentheses nested deeper than FreshRSS accepts.
	ErrTooDeep = errors.New("search: parentheses are nested too deeply")
	// ErrRegexp is returned for a query with a regular expression that Go's
	// regexp package does not accept, such as one with a backreference or a
	// lookahead.
	ErrRegexp = errors.New("search: regular expression is not supported")
)

// SavedQuery is a search the user has saved. Queries refer to it by its
// position in the list (S:0) or by its name (search:name).
type SavedQuery struct {
	Name   string
	Search string
}

// Options are what a query is read against.
type Options struct {
	// Queries are the saved searches of the user, in the order of their settings.
	Queries []SavedQuery
	// Now is the moment relative dates such as date:P1W count from, and its
	// location the time zone of dates written without one.
	Now time.Time
	// Labels makes L: and labels: demands on the labels of an entry, as a
	// search of the database of FreshRSS does. Filters leave it off: they run
	// before an entry has labels, and FreshRSS passes the operators over there.
	Labels bool
}

// Operators that join a parenthesized part to what precedes it.
const (
	opAnd    = "AND"
	opOr     = "OR"
	opAndNot = "AND NOT"
	opOrNot  = "OR NOT"
)

// Query is a parsed search. It holds either parts in parentheses, joined by
// their operators, or alternatives, any of which is enough.
type Query struct {
	op    string
	parts []*Query
	terms []*term
}

type parser struct {
	opts Options
	// err is the first regular expression that did not compile.
	err error
}

// Parse reads a query. A query that makes no demands, an empty one included,
// matches every entry.
func Parse(input string, opts Options) (*Query, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	p := &parser{opts: opts}
	q, err := p.boolean(input, 0, opAnd)
	if err != nil {
		return nil, err
	}
	if p.err != nil {
		return nil, p.err
	}
	return q, nil
}

// And returns a query that matches what both queries match. A nil query
// makes no demands.
func And(a, b *Query) *Query {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	left, right := *a, *b
	left.op, right.op = opAnd, opAnd
	return &Query{parts: []*Query{&left, &right}}
}

// phpSpace is what PHP's trim removes.
const phpSpace = " \n\r\t\v\x00"

var (
	orWord       = regexp.MustCompile(`(?i)\bOR\b`)
	trailingOr   = regexp.MustCompile(`(?i)\bOR$`)
	leadingOr    = regexp.MustCompile(`(?i)^OR\b`)
	openingParen = regexp.MustCompile(`(?:^|[^\\])\(`)

	namedQueryQuoted = regexp.MustCompile(`\bsearch:(?:"(.*?)"|'(.*?)')`)
	namedQueryBare   = regexp.MustCompile(`\bsearch:([^ \t\n\v\f\r"']*)`)
	numberedQuery    = regexp.MustCompile(`\bS:([0-9,]+)`)
)

func (p *parser) boolean(input string, level int, op string) (*Query, error) {
	q := &Query{op: op}
	input = strings.Trim(input, phpSpace)
	input = strings.TrimLeft(input, " )")
	input = strings.TrimRight(input, ` (\`)
	if input == "" {
		return q, nil
	}
	if level == 0 {
		if len(input) > maxLength {
			return nil, ErrTooLong
		}
		input = escapeLiterals(input)
		input = p.expandNamedQueries(input)
		input = p.expandNumberedQueries(input)
		input = strings.Trim(input, phpSpace)
	}
	input, err := consistentOrParentheses(input)
	if err != nil {
		return nil, err
	}
	done, err := p.parentheses(q, input, level)
	if err != nil {
		return nil, err
	}
	if !done {
		p.orSegments(q, input)
	}
	return q, nil
}

// escapeLiterals hides the parentheses and the word OR inside quoted strings
// and regular expressions from the parsing of the Boolean structure.
func escapeLiterals(input string) string {
	var b strings.Builder
	for i := 0; i < len(input); {
		c := input[i]
		if (c == '\'' || c == '"' || c == '/') && (i == 0 || strings.IndexByte(" \t\n\v\f\r(:#!-", input[i-1]) >= 0) {
			if end := literalEnd(input, i); end > 0 {
				lit := strings.NewReplacer("(", `\u0028`, ")", `\u0029`).Replace(input[i:end])
				b.WriteString(orWord.ReplaceAllStringFunc(lit, func(or string) string {
					return strings.NewReplacer("O", `\u004f`, "o", `\u006f`, "R", `\u0052`, "r", `\u0072`).Replace(or)
				}))
				i = end
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// literalEnd returns the end of the literal opened by the delimiter at start:
// past the next same delimiter that has at least one character before it and
// no backslash, and past the flags i and m after it. Zero means there is none
// on the line.
func literalEnd(input string, start int) int {
	for k := start + 1; k < len(input); k++ {
		if k >= start+2 && input[k] == input[start] && input[k-1] != '\\' {
			end := k + 1
			for end < len(input) && (input[end] == 'i' || input[end] == 'm') {
				end++
			}
			return end
		}
		if input[k] == '\n' {
			return 0
		}
	}
	return 0
}

func unescapeLiterals(input string) string {
	return strings.NewReplacer(`\u0028`, "(", `\u0029`, ")", `\u004f`, "O", `\u006f`, "o", `\u0052`, "R", `\u0072`, "r").Replace(input)
}

// notEmpty is PHP's !empty for a string.
func notEmpty(s string) bool {
	return s != "" && s != "0"
}

// replaceEach is PHP's str_replace with lists: every occurrence of each
// string is replaced in turn, in what the earlier replacements left.
func replaceEach(input string, from, to []string) string {
	for i, f := range from {
		if f != "" {
			input = strings.ReplaceAll(input, f, to[i])
		}
	}
	return input
}

// expandNamedQueries puts saved searches in place of search:name.
func (p *parser) expandNamedQueries(input string) string {
	sets := [][][]string{namedQueryQuoted.FindAllStringSubmatch(input, -1), namedQueryBare.FindAllStringSubmatch(input, -1)}
	if len(sets[0])+len(sets[1]) == 0 {
		return input
	}
	queries := map[string]string{}
	for _, saved := range p.opts.Queries {
		if saved.Name != "" && saved.Search != "" {
			queries[saved.Name] = strings.Trim(saved.Search, phpSpace)
		}
	}
	var from, to []string
	for _, matches := range sets {
		for i := len(matches) - 1; i >= 0; i-- {
			m := matches[i]
			name := unescapeLiterals(strings.Trim(firstGroup(m), phpSpace))
			from = append(from, m[0])
			if search := queries[name]; notEmpty(search) {
				to = append(to, "("+escapeLiterals(search)+")")
			} else {
				to = append(to, "")
			}
		}
	}
	return replaceEach(input, from, to)
}

// firstGroup returns the first non-empty capture of a match.
func firstGroup(m []string) string {
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// expandNumberedQueries puts saved searches in place of S:1,2.
func (p *parser) expandNumberedQueries(input string) string {
	matches := numberedQuery.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input
	}
	var from, to []string
	for i := len(matches) - 1; i >= 0; i-- {
		var found []string
		for _, id := range strings.Split(matches[i][1], ",") {
			// An empty number counts as zero, as intval makes it.
			n, err := strconv.Atoi(id)
			if (err != nil && id != "") || n >= len(p.opts.Queries) {
				continue
			}
			if search := strings.Trim(p.opts.Queries[n].Search, phpSpace); notEmpty(search) {
				found = append(found, escapeLiterals(search))
			}
		}
		from = append(from, matches[i][0])
		if len(found) > 0 {
			to = append(to, "(("+strings.Join(found, ") OR (")+"))")
		} else {
			to = append(to, "")
		}
	}
	return replaceEach(input, from, to)
}

// splitOr cuts the input at the word OR and keeps the word between the pieces.
func splitOr(input string) []string {
	var out []string
	last := 0
	for _, loc := range orWord.FindAllStringIndex(input, -1) {
		out = append(out, input[last:loc[0]], input[loc[0]:loc[1]])
		last = loc[1]
	}
	return append(out, input[last:])
}

func quoteCount(s string) int {
	return strings.Count(s, `"`) + strings.Count(s, "&quot;")
}

func isNegation(s string) bool {
	return s == "!" || s == "-"
}

// endsWithNegation is the pattern /[!-]$/ of PCRE, whose $ also stands before
// a final line break.
func endsWithNegation(s string) bool {
	s = strings.TrimSuffix(s, "\n")
	return strings.HasSuffix(s, "!") || strings.HasSuffix(s, "-")
}

// addOrParentheses turns `ab cd OR ef OR "gh ij"` into `(ab cd) OR (ef) OR ("gh ij")`.
func addOrParentheses(input string) string {
	input = strings.Trim(input, phpSpace)
	if input == "" {
		return ""
	}
	splits := splitOr(input)
	if len(splits) <= 1 {
		return input
	}
	result, segment := "", ""
	for _, piece := range splits {
		segment += piece
		switch {
		case strings.Trim(segment, phpSpace) == "":
			segment = ""
		case strings.EqualFold(segment, "OR"):
			result += segment + " "
			segment = ""
		case quoteCount(segment)%2 == 0:
			segment = strings.Trim(segment, phpSpace)
			if isNegation(segment) {
				result += segment
			} else {
				result += "(" + segment + ") "
			}
			segment = ""
		}
	}
	segment = strings.Trim(segment, phpSpace)
	if isNegation(segment) {
		result += segment
	} else if segment != "" {
		result += "(" + segment + ")"
	}
	return strings.Trim(result, phpSpace)
}

// consistentOrParentheses adds parentheses around the operands of OR where a
// query mixes OR with and without them: `(ab (cd OR ef)) OR gh` becomes
// `(ab ((cd) OR (ef))) OR (gh)`.
func consistentOrParentheses(input string) (string, error) {
	if len(input) > maxLength {
		return "", ErrTooLong
	}
	if !openingParen.MatchString(input) {
		return strings.Trim(input, phpSpace), nil
	}
	depth := 0
	result, segment := "", ""
	for i := 0; i < len(input); i++ {
		c := input[i : i+1]
		if i == 0 || input[i-1] != '\\' {
			switch c {
			case "(":
				if depth == 0 {
					if segment != "" {
						result = strings.TrimRight(result, phpSpace) + " " + addOrParentheses(segment)
						if !endsWithNegation(result) {
							result += " "
						}
						segment = ""
					}
					c = ""
				}
				if depth >= maxDepth {
					return "", ErrTooDeep
				}
				depth++
			case ")":
				depth--
				if depth == 0 {
					inner, err := consistentOrParentheses(segment)
					if err != nil {
						return "", err
					}
					segment = inner
					if segment != "" {
						result += "(" + segment + ")"
						segment = ""
					}
					c = ""
				}
			}
		}
		segment += c
	}
	if strings.Trim(segment, phpSpace) != "" {
		result = strings.TrimRight(result, phpSpace)
		if !endsWithNegation(segment) {
			result += " "
		}
		result += addOrParentheses(segment)
	}
	return strings.Trim(result, phpSpace), nil
}

// parentheses reads the input as parts in parentheses and the text between
// them. It reports false when the input has no parentheses to go by.
func (p *parser) parentheses(q *Query, input string, level int) (bool, error) {
	input = strings.Trim(input, phpSpace)
	before := ""
	found := false
	next := opAnd
	add := func(text, op string) error {
		part, err := p.boolean(text, level+1, op)
		if err != nil {
			return err
		}
		if len(part.parts)+len(part.terms) > 0 {
			q.parts = append(q.parts, part)
		}
		return nil
	}
	for i := 0; i < len(input); i++ {
		if input[i] != '(' || (i >= 1 && input[i-1] == '\\') {
			before += input[i : i+1]
			continue
		}
		found = true
		before = strings.Trim(before, phpSpace)
		switch {
		case endsWithNegation(before):
			before = strings.TrimRight(before, " !-")
			isOr := trailingOr.MatchString(before)
			if isOr {
				before = before[:len(before)-2]
			}
			if err := add(before, next); err != nil {
				return false, err
			}
			before = ""
			next = opAndNot
			if isOr {
				next = opOrNot
			}
		case trailingOr.MatchString(before):
			if err := add(before[:len(before)-2], next); err != nil {
				return false, err
			}
			before = ""
			next = opOr
		case before != "":
			if err := add(before, next); err != nil {
				return false, err
			}
			before = ""
		}

		// Up to the matching closing parenthesis.
		open := 1
		sub := ""
		for i++; i < len(input); i++ {
			c := input[i]
			escaped := input[i-1] == '\\'
			if c == '(' && !escaped {
				open++
			} else if c == ')' && !escaped {
				open--
				if open == 0 {
					if err := add(sub, next); err != nil {
						return false, err
					}
					next = opAnd
					break
				}
			}
			sub += input[i : i+1]
		}
	}
	if !found {
		return false, nil
	}
	before = strings.Trim(before, phpSpace)
	if leadingOr.MatchString(before) {
		next = opOr
		before = before[2:]
	}
	return true, add(before, next)
}

// orSegments reads the input as alternatives separated by OR.
func (p *parser) orSegments(q *Query, input string) {
	input = strings.Trim(input, phpSpace)
	if input == "" {
		return
	}
	segment := ""
	for _, piece := range splitOr(input) {
		segment += piece
		if strings.Trim(segment, phpSpace) == "" || strings.EqualFold(segment, "OR") {
			segment = ""
		} else if quoteCount(segment)%2 == 0 {
			q.terms = append(q.terms, p.term(strings.Trim(segment, phpSpace)))
			segment = ""
		}
	}
	if segment = strings.Trim(segment, phpSpace); segment != "" {
		q.terms = append(q.terms, p.term(segment))
	}
}
