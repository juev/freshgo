package store

import "strings"

// CategoryNamed returns the category a name matches, or nil. See pickName.
func CategoryNamed(categories []*Category, name string) *Category {
	i := pickName(len(categories), func(i int) (int64, string) { return categories[i].ID, categories[i].Name }, name)
	if i < 0 {
		return nil
	}
	return categories[i]
}

// TagNamed returns the label a name matches, or nil. See pickName.
func TagNamed(tags []*Tag, name string) *Tag {
	i := pickName(len(tags), func(i int) (int64, string) { return tags[i].ID, tags[i].Name }, name)
	if i < 0 {
		return nil
	}
	return tags[i]
}

// NamedCategoryOrTag resolves a name that may be of a category or of a label,
// as user/-/label/<name> of the Google Reader API is: a row of exactly that
// name, whichever table it is in, wins over one that differs in case; else
// the category found by CategoryNamed, else the label found by TagNamed. At
// most one of the two results is not nil.
func NamedCategoryOrTag(categories []*Category, tags []*Tag, name string) (*Category, *Tag) {
	c, t := CategoryNamed(categories, name), TagNamed(tags, name)
	if t != nil && t.Name == name && (c == nil || c.Name != name) {
		return nil, t
	}
	if c != nil {
		return c, nil
	}
	return nil, t
}

// pickName is the one way a name of a category or of a label is matched:
// ignoring case, by Unicode folding, so that "News" and "news", "Новости" and
// "новости" are one name. It is done here and not in SQL because SQLite folds
// ASCII only. Of several names that differ in case the exact one wins, then
// the oldest, the one of the lowest identifier; pairs stored already are not
// merged. It returns the index of the match, or -1.
func pickName(n int, at func(i int) (id int64, name string), want string) int {
	best, bestID := -1, int64(0)
	for i := range n {
		id, name := at(i)
		if name == want {
			return i
		}
		if strings.EqualFold(name, want) && (best < 0 || id < bestID) {
			best, bestID = i, id
		}
	}
	return best
}
