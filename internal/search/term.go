package search

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// pattern is a regular expression of a query: /…/ with the flags i and m.
type pattern struct {
	// source is the expression as written, delimiters and flags included.
	source string
	re     *regexp.Regexp
}

// texts are the demands a term makes on one text of an entry.
type texts struct {
	// plain are strings the text has to contain, regex expressions it has to match.
	plain []string
	regex []*pattern
}

// span is a time interval; zero stands for an open end.
type span struct {
	min, max int64
}

// term is one alternative of a query: demands that all have to hold. The
// fields starting with "not" are the negated ones. A nil list of
// identifiers is no demand, an empty one a demand nothing meets.
type term struct {
	entryIDs, notEntryIDs       []string
	feedIDs, notFeedIDs         []int64
	categoryIDs, notCategoryIDs []int64
	// Labels are always read, so that they are not taken for words, and
	// count when byLabel is set. A list of identifiers is []int64, or "*"
	// for any label; the entry needs a label of every list.
	byLabel                   bool
	labelIDs, notLabelIDs     []any
	labelNames, notLabelNames [][]string

	intitle, notIntitle texts
	intext, notIntext   texts
	author, notAuthor   texts
	inurl, notInurl     texts
	tags, notTags       texts
	search, notSearch   texts

	date, notDate         span
	pubdate, notPubdate   span
	modified, notModified span
	userdate, notUserdate span
}

// The parser of FreshRSS is a series of regular expressions, each cutting
// what it recognizes out of the input. The expressions below are the same
// ones without the lookbehind assertions Go lacks: where FreshRSS asks for
// white space, a parenthesis or the start before a match, the character is
// matched and the group "m" holds the match proper.
const (
	// A regular expression literal: up to the first slash without a backslash before it.
	regexLiteral = `/(?:[^/\n]*\\/)*(?:[^/\n]*[^/\\\n])?/[im]*`
	quoted       = `(?:"(.*?)"|'(.*?)')`
	negated      = `(?:^|[\s(])(?P<m>[!-]`
)

// operator holds the three forms of an operator that takes text.
type operator struct {
	regex, quoted, bare *regexp.Regexp
}

func positive(prefix, bare string) operator {
	return operator{
		regex:  regexp.MustCompile(prefix + `(` + regexLiteral + `)`),
		quoted: regexp.MustCompile(prefix + quoted),
		bare:   regexp.MustCompile(prefix + bare),
	}
}

func negative(name, bare string) operator {
	return operator{
		regex:  regexp.MustCompile(negated + name + `(` + regexLiteral + `))`),
		quoted: regexp.MustCompile(negated + name + quoted + `)`),
		bare:   regexp.MustCompile(negated + name + bare + `)`),
	}
}

var (
	spaces = regexp.MustCompile(`[ \t\n\v\f\r]+`)

	entryIDs       = regexp.MustCompile(`\be:([0-9,]*)`)
	notEntryIDs    = regexp.MustCompile(negated + `e:([0-9,]*))`)
	feedIDs        = regexp.MustCompile(`\bf:([0-9,]*)`)
	notFeedIDs     = regexp.MustCompile(negated + `f:([0-9,]*))`)
	categoryIDs    = regexp.MustCompile(`\bc:([0-9,]*)`)
	notCategoryIDs = regexp.MustCompile(negated + `c:([0-9,]*))`)
	labelIDs       = regexp.MustCompile(`\b[lL]:([0-9,]+|[*])`)
	notLabelIDs    = regexp.MustCompile(negated + `[lL]:([0-9,]+|[*]))`)
	queryIDs       = regexp.MustCompile(`\bS:([0-9,]+|[*])`)
	notQueryIDs    = regexp.MustCompile(negated + `S:([0-9,]+|[*]))`)

	labelNames    = positive(`\blabels?:`, `([^\s"]*)`)
	notLabelNames = negative(`labels?:`, `([^\s"]*)`)
	queryNames    = positive(`\bsearch?:`, `([^\s"]*)`)
	notQueryNames = negative(`search?:`, `([^\s"]*)`)

	intitle    = positive(`\bintitle:`, `([^\s"]*)`)
	notIntitle = negative(`intitle:`, `([^\s"]*)`)
	intext     = positive(`\bintext:`, `([^\s"]*)`)
	notIntext  = negative(`intext:`, `([^\s"]*)`)
	author     = positive(`\bauthor:`, `([^\s"]*)`)
	notAuthor  = negative(`author:`, `([^\s"]*)`)
	inurl      = positive(`\binurl:`, `([^\s"]*)`)
	notInurl   = negative(`inurl:`, `([^\s"]*)`)
	tags       = positive(`#`, `([^\s"]+)`)
	notTags    = negative(`#`, `([^\s"]+)`)

	dates       = regexp.MustCompile(`\bdate:(\S*)`)
	notDates    = regexp.MustCompile(negated + `date:(\S*))`)
	pubdates    = regexp.MustCompile(`\bpubdate:(\S*)`)
	notPubdates = regexp.MustCompile(negated + `pubdate:(\S*))`)
	mdates      = regexp.MustCompile(`\bmdate:(\S*)`)
	notMdates   = regexp.MustCompile(negated + `mdate:(\S*))`)
	userdates   = regexp.MustCompile(`\buserdate:(\S*)`)
	notUserdate = regexp.MustCompile(negated + `userdate:(\S*))`)

	// A free regular expression or quoted string stands after white space or
	// at the start; FreshRSS rules the parenthesis out here.
	freeRegex     = regexp.MustCompile(`(?:^|\s)(?P<m>(` + regexLiteral + `))`)
	freeQuoted    = regexp.MustCompile(`(?:^|\s)(?P<m>` + quoted + `)`)
	notFreeRegex  = regexp.MustCompile(negated + `(` + regexLiteral + `))`)
	notFreeQuoted = regexp.MustCompile(negated + quoted + `)`)
	notFreeWord   = regexp.MustCompile(negated + `(\S+))`)
)

// clean collapses white space and trims the ends.
func clean(input string) string {
	return strings.Trim(spaces.ReplaceAllString(input, " "), phpSpace)
}

// cut finds what the expression matches in the input, removes every
// occurrence of each matched text and returns the captured values; ok tells
// whether anything matched.
func cut(input *string, re *regexp.Regexp) (values []string, ok bool) {
	matches := re.FindAllStringSubmatchIndex(*input, -1)
	if len(matches) == 0 {
		return nil, false
	}
	whole := max(re.SubexpIndex("m"), 0)
	var texts []string
	for _, m := range matches {
		texts = append(texts, (*input)[m[2*whole]:m[2*whole+1]])
		value := ""
		for g := 1; g < len(m)/2; g++ {
			if g != whole && m[2*g] >= 0 {
				value = (*input)[m[2*g]:m[2*g+1]]
				break
			}
		}
		values = append(values, value)
	}
	for _, text := range texts {
		if text != "" {
			*input = strings.ReplaceAll(*input, text, "")
		}
	}
	return values, true
}

func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// idLists splits the captured lists of identifiers.
func idLists(values []string) [][]string {
	var lists [][]string
	for _, v := range values {
		if ids := nonEmpty(strings.Split(v, ",")); len(ids) > 0 {
			lists = append(lists, ids)
		}
	}
	return lists
}

func numbers(ids []string) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		// Beyond the range of an integer PHP's intval gives the largest one.
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			n = 1<<63 - 1
		}
		out = append(out, n)
	}
	return out
}

func cutStrings(input *string, re *regexp.Regexp) []string {
	values, ok := cut(input, re)
	if !ok {
		return nil
	}
	out := []string{}
	for _, ids := range idLists(values) {
		out = append(out, ids...)
	}
	return out
}

func cutNumbers(input *string, re *regexp.Regexp) []int64 {
	ids := cutStrings(input, re)
	if ids == nil {
		return nil
	}
	return numbers(ids)
}

// cutGroups reads lists of numbers that stay apart, or "*" for any; the
// lists after a "*" are dropped.
func cutGroups(input *string, re *regexp.Regexp) []any {
	values, ok := cut(input, re)
	if !ok {
		return nil
	}
	out := []any{}
	for _, v := range values {
		if v == "*" {
			out = append(out, "*")
			break
		}
		if ids := nonEmpty(strings.Split(v, ",")); len(ids) > 0 {
			out = append(out, numbers(ids))
		}
	}
	return out
}

func cutNames(input *string, op operator) [][]string {
	var lists []string
	if values, ok := cut(input, op.quoted); ok {
		lists = values
	}
	if values, ok := cut(input, op.bare); ok {
		lists = append(lists, values...)
	}
	if len(lists) == 0 {
		return nil
	}
	return idLists(lists)
}

func (p *parser) compile(sources []string) []*pattern {
	var out []*pattern
	for _, source := range sources {
		end := strings.LastIndexByte(source, '/')
		expr := strings.ReplaceAll(source[1:end], `\/`, "/")
		if flags := source[end+1:]; flags != "" {
			expr = "(?" + flags + ")" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil && p.err == nil {
			p.err = fmt.Errorf("%w: %s: %w", ErrRegexp, source, err)
		}
		out = append(out, &pattern{source: source, re: re})
	}
	return out
}

func (p *parser) cutTexts(input *string, op operator) texts {
	var t texts
	if values, ok := cut(input, op.regex); ok {
		t.regex = p.compile(values)
	}
	if values, ok := cut(input, op.quoted); ok {
		t.plain = values
	}
	if values, ok := cut(input, op.bare); ok {
		t.plain = append(t.plain, values...)
	}
	t.plain = nonEmpty(t.plain)
	return t
}

func (p *parser) cutTags(input *string, op operator) texts {
	t := p.cutTexts(input, op)
	for i, tag := range t.plain {
		t.plain[i] = strings.Trim(strings.ReplaceAll(tag, "+", " "), phpSpace)
	}
	return t
}

func (p *parser) cutSpan(input *string, re *regexp.Regexp) span {
	values, _ := cut(input, re)
	if values = nonEmpty(values); len(values) == 0 || !notEmpty(values[0]) {
		return span{}
	}
	return p.interval(values[0])
}

// term reads one alternative of a query, in the order FreshRSS does: each
// step sees what the earlier ones left.
func (p *parser) term(input string) *term {
	input = clean(input)
	input = strings.NewReplacer(`\(`, "(", `\)`, ")").Replace(input)
	input = unescapeLiterals(input)
	t := &term{byLabel: p.opts.Labels}

	t.notEntryIDs = cutStrings(&input, notEntryIDs)
	t.notFeedIDs = cutNumbers(&input, notFeedIDs)
	t.notCategoryIDs = cutNumbers(&input, notCategoryIDs)
	t.notLabelIDs = cutGroups(&input, notLabelIDs)
	t.notLabelNames = cutNames(&input, notLabelNames)
	// Saved searches were expanded before the query was split; what is left
	// of them here names nothing and is dropped.
	cutGroups(&input, notQueryIDs)
	cutNames(&input, notQueryNames)

	t.notUserdate = p.cutSpan(&input, notUserdate)
	t.notModified = p.cutSpan(&input, notMdates)
	t.notPubdate = p.cutSpan(&input, notPubdates)
	t.notDate = p.cutSpan(&input, notDates)

	t.notIntitle = p.cutTexts(&input, notIntitle)
	t.notIntext = p.cutTexts(&input, notIntext)
	t.notAuthor = p.cutTexts(&input, notAuthor)
	t.notInurl = p.cutTexts(&input, notInurl)
	t.notTags = p.cutTags(&input, notTags)

	t.entryIDs = cutStrings(&input, entryIDs)
	t.feedIDs = cutNumbers(&input, feedIDs)
	t.categoryIDs = cutNumbers(&input, categoryIDs)
	t.labelIDs = cutGroups(&input, labelIDs)
	t.labelNames = cutNames(&input, labelNames)
	cutGroups(&input, queryIDs)
	cutNames(&input, queryNames)

	t.userdate = p.cutSpan(&input, userdates)
	t.modified = p.cutSpan(&input, mdates)
	t.pubdate = p.cutSpan(&input, pubdates)
	t.date = p.cutSpan(&input, dates)

	t.intitle = p.cutTexts(&input, intitle)
	t.intext = p.cutTexts(&input, intext)
	t.author = p.cutTexts(&input, author)
	t.inurl = p.cutTexts(&input, inurl)
	t.tags = p.cutTags(&input, tags)

	// Free text: quoted strings and regular expressions first, then what is
	// negated, then the remaining words.
	if input = clean(input); input == "" {
		return t
	}
	if values, ok := cut(&input, freeRegex); ok {
		t.search.regex = p.compile(values)
	}
	if values, ok := cut(&input, freeQuoted); ok {
		t.search.plain = values
	}
	if input = clean(input); input == "" {
		return t
	}
	if values, ok := cut(&input, notFreeRegex); ok {
		t.notSearch.regex = p.compile(values)
	}
	if values, ok := cut(&input, notFreeQuoted); ok {
		t.notSearch.plain = values
	}
	if input = clean(input); input == "" {
		return t
	}
	if values, ok := cut(&input, notFreeWord); ok {
		t.notSearch.plain = append(t.notSearch.plain, values...)
	}
	t.notSearch.plain = nonEmpty(t.notSearch.plain)
	if input = clean(input); input != "" {
		t.search.plain = append(t.search.plain, strings.Split(input, " ")...)
	}
	return t
}
