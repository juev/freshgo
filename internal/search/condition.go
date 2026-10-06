package search

import "strconv"

// ConditionKind says what a Condition tests.
type ConditionKind int

const (
	// CondAnd holds when every condition in Of does; with none, always.
	CondAnd ConditionKind = iota
	// CondOr holds when a condition in Of does; with none, never.
	CondOr
	// CondNot holds when the only condition in Of does not.
	CondNot
	// CondEntries, CondFeeds and CondCategories hold when the identifier of
	// the entry, of its feed, of the category of its feed is one of IDs.
	CondEntries
	CondFeeds
	CondCategories
	// CondLabels holds when the entry has a label whose identifier is one of
	// IDs, CondLabelNames when it has one named as one of Names, CondAnyLabel
	// when it has a label at all.
	CondLabels
	CondLabelNames
	CondAnyLabel
	// CondAdded, CondPublished, CondModified and CondUserModified hold when
	// the time is from Min to Max, both included and in Unix seconds; zero
	// leaves the end open. The time an entry was added is its identifier,
	// which is in microseconds: the bounds are Min and Max times 1 000 000.
	CondAdded
	CondPublished
	CondModified
	CondUserModified
)

// Condition is a test on what a database keeps of an entry apart from its
// texts: identifiers, labels and times.
type Condition struct {
	Kind     ConditionKind
	Of       []*Condition
	IDs      []int64
	Names    []string
	Min, Max int64
}

func always() *Condition { return &Condition{Kind: CondAnd} }
func never() *Condition  { return &Condition{Kind: CondOr} }

func (c *Condition) isAlways() bool { return c.Kind == CondAnd && len(c.Of) == 0 }
func (c *Condition) isNever() bool  { return c.Kind == CondOr && len(c.Of) == 0 }

func both(a, b *Condition) *Condition {
	switch {
	case a.isNever() || b.isAlways():
		return a
	case b.isNever() || a.isAlways():
		return b
	}
	return &Condition{Kind: CondAnd, Of: []*Condition{a, b}}
}

func either(a, b *Condition) *Condition {
	switch {
	case a.isAlways() || b.isNever():
		return a
	case b.isAlways() || a.isNever():
		return b
	}
	return &Condition{Kind: CondOr, Of: []*Condition{a, b}}
}

func not(c *Condition) *Condition {
	switch {
	case c.isAlways():
		return never()
	case c.isNever():
		return always()
	}
	return &Condition{Kind: CondNot, Of: []*Condition{c}}
}

func oneOf(kind ConditionKind, ids []int64) *Condition {
	if len(ids) == 0 {
		return never()
	}
	return &Condition{Kind: kind, IDs: ids}
}

// Condition returns a test every entry the query matches passes. It is as
// narrow as the query allows without looking at texts, so that a database
// can leave out the entries that cannot match before Match reads the rest.
func (q *Query) Condition() *Condition {
	c, _ := q.condition()
	if c.size() > maxConditionSize {
		return always()
	}
	return c
}

// maxConditionSize bounds the test handed to a database: SQLite refuses an
// expression nested deeper than 1000. A query that asks for more gets no
// test at all, which is still one every matching entry passes.
const maxConditionSize = 500

// size is the number of conditions in the tree.
func (c *Condition) size() int {
	n := 1
	for _, sub := range c.Of {
		n += sub.size()
	}
	return n
}

// condition also tells whether the test is exact: passed by the entries the
// query matches and by no other. Only an exact test can be denied.
func (q *Query) condition() (c *Condition, exact bool) {
	if len(q.terms) > 0 {
		c, exact = never(), true
		for _, t := range q.terms {
			tc, tExact := t.condition()
			c, exact = either(c, tc), exact && tExact
		}
		return c, exact
	}
	c, exact = always(), true
	for _, part := range q.parts {
		pc, pExact := part.condition()
		switch part.op {
		case opOr:
			c = either(c, pc)
		case opAndNot:
			if pExact {
				c = both(c, not(pc))
			}
		case opOrNot:
			if pExact {
				c = either(c, not(pc))
			} else {
				c = always()
			}
		default:
			c = both(c, pc)
		}
		exact = exact && pExact
	}
	return c, exact
}

func (t *term) condition() (c *Condition, exact bool) {
	c = always()
	add := func(d *Condition) { c = both(c, d) }

	if t.entryIDs != nil {
		add(oneOf(CondEntries, entryNumbers(t.entryIDs)))
	}
	if t.notEntryIDs != nil {
		add(not(oneOf(CondEntries, entryNumbers(t.notEntryIDs))))
	}
	if t.feedIDs != nil {
		add(oneOf(CondFeeds, t.feedIDs))
	}
	if t.notFeedIDs != nil {
		add(not(oneOf(CondFeeds, t.notFeedIDs)))
	}
	if t.categoryIDs != nil {
		add(oneOf(CondCategories, t.categoryIDs))
	}
	if t.notCategoryIDs != nil {
		add(not(oneOf(CondCategories, t.notCategoryIDs)))
	}
	if t.byLabel {
		for _, list := range t.labelIDs {
			add(labelled(list))
		}
		for _, list := range t.notLabelIDs {
			add(not(labelled(list)))
		}
		for _, names := range t.labelNames {
			add(&Condition{Kind: CondLabelNames, Names: names})
		}
		for _, names := range t.notLabelNames {
			add(not(&Condition{Kind: CondLabelNames, Names: names}))
		}
	}
	for _, d := range []struct {
		kind         ConditionKind
		in, excluded span
	}{
		{CondAdded, t.date, t.notDate},
		{CondPublished, t.pubdate, t.notPubdate},
		{CondModified, t.modified, t.notModified},
		{CondUserModified, t.userdate, t.notUserdate},
	} {
		if d.in != (span{}) {
			add(&Condition{Kind: d.kind, Min: d.in.min, Max: d.in.max})
		}
		// What span.outside asks: before the lower bound and after the upper one.
		if d.excluded.min != 0 {
			add(not(&Condition{Kind: d.kind, Min: d.excluded.min}))
		}
		if d.excluded.max != 0 {
			add(not(&Condition{Kind: d.kind, Max: d.excluded.max}))
		}
	}

	for _, texts := range []texts{
		t.intitle, t.notIntitle, t.intext, t.notIntext, t.author, t.notAuthor,
		t.inurl, t.notInurl, t.tags, t.notTags, t.search, t.notSearch,
	} {
		if len(texts.plain)+len(texts.regex) > 0 {
			return c, false
		}
	}
	return c, true
}

func labelled(list any) *Condition {
	if ids, ok := list.([]int64); ok {
		return oneOf(CondLabels, ids)
	}
	return &Condition{Kind: CondAnyLabel}
}

// entryNumbers returns the identifiers among the strings a query gave for
// entries. Matching compares them as written, so a string that is not how an
// identifier is printed names no entry.
func entryNumbers(ids []string) []int64 {
	var out []int64
	for _, id := range ids {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && strconv.FormatInt(n, 10) == id {
			out = append(out, n)
		}
	}
	return out
}

// UsesLabels reports whether matching the query looks at the labels of an entry.
func (q *Query) UsesLabels() bool {
	for _, part := range q.parts {
		if part.UsesLabels() {
			return true
		}
	}
	for _, t := range q.terms {
		if t.byLabel && len(t.labelIDs)+len(t.notLabelIDs)+len(t.labelNames)+len(t.notLabelNames) > 0 {
			return true
		}
	}
	return false
}
