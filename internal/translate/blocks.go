// Package translate has the text of an entry translated by a language model
// behind an OpenAI-compatible chat endpoint. The model gets paragraphs of
// text with numbered marks where the elements inside them stand; it never
// sees the markup and cannot change it. See docs/specs/translate.md.
package translate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// inline are the elements that stand inside a sentence; every other
// element ends the sentence around it. A line break does: there are texts
// whose paragraphs are parted by breaks alone.
var inline = map[atom.Atom]bool{
	atom.A: true, atom.Abbr: true, atom.B: true, atom.Bdi: true, atom.Bdo: true, atom.Cite: true, atom.Code: true,
	atom.Data: true, atom.Del: true, atom.Dfn: true, atom.Em: true, atom.I: true, atom.Img: true, atom.Ins: true, atom.Kbd: true,
	atom.Mark: true, atom.Q: true, atom.S: true, atom.Samp: true, atom.Small: true, atom.Span: true, atom.Strong: true,
	atom.Sub: true, atom.Sup: true, atom.Time: true, atom.U: true, atom.Var: true, atom.Wbr: true, atom.Font: true, atom.Tt: true,
}

func isInline(n *html.Node) bool {
	return n.Type != html.ElementNode || inline[n.DataAtom]
}

// unit is a run of text with the elements inside it: what a reader takes
// for one sentence or paragraph.
type unit struct {
	parent *html.Node
	nodes  []*html.Node
	// marked is the text of the unit with its elements as numbered marks:
	// <1>…</1> for an element with content to translate, <2/> for one
	// without, such as an image or a piece of code.
	marked string
	// marks are the elements by their number, from 1.
	marks []*html.Node
	// plain is the text alone, to tell an answer that translated nothing.
	plain string
}

var letters = regexp.MustCompile(`\p{L}{2}`)

// units cuts the content of a node into what is translated as one.
func units(root *html.Node) []*unit {
	var out []*unit
	var walk func(parent *html.Node)
	walk = func(parent *html.Node) {
		var run []*html.Node
		flush := func() {
			nodes := run
			run = nil
			u := &unit{parent: parent, nodes: nodes}
			var marked, plain strings.Builder
			var write func(n *html.Node)
			write = func(n *html.Node) {
				switch {
				case n.Type == html.TextNode:
					marked.WriteString(n.Data)
					plain.WriteString(n.Data)
				case n.Type != html.ElementNode:
				case whole(n):
					u.marks = append(u.marks, n)
					fmt.Fprintf(&marked, "<%d/>", len(u.marks))
				default:
					u.marks = append(u.marks, n)
					number := len(u.marks)
					fmt.Fprintf(&marked, "<%d>", number)
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						write(c)
					}
					fmt.Fprintf(&marked, "</%d>", number)
				}
			}
			for _, n := range nodes {
				write(n)
			}
			// Without two letters in a row there is nothing to translate:
			// numbers, marks of footnotes, punctuation.
			if !letters.MatchString(plain.String()) {
				return
			}
			u.marked, u.plain = strings.Join(strings.Fields(marked.String()), " "), strings.Join(strings.Fields(plain.String()), " ")
			out = append(out, u)
		}
		var children []*html.Node
		for c := parent.FirstChild; c != nil; c = c.NextSibling {
			children = append(children, c)
		}
		for _, c := range children {
			if isInline(c) && !hasBlock(c) {
				run = append(run, c)
				continue
			}
			flush()
			if c.Type == html.ElementNode && c.DataAtom != atom.Pre {
				walk(c)
			}
		}
		flush()
	}
	walk(root)
	return out
}

// hasBlock reports whether an inline element holds one that is not, as a
// link around a picture with a caption may.
func hasBlock(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if !isInline(c) || hasBlock(c) {
			return true
		}
	}
	return false
}

var mark = regexp.MustCompile(`<(/?)(\d+)(/?)>`)

// put replaces the nodes of a unit with its translation: the marks of
// the answer become the elements they stand for, with the attributes and,
// for those without content to translate, the content they had. It
// returns what is wrong with the answer, and changes nothing then.
func (u *unit) put(answer string) string {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "empty"
	}
	holder := &html.Node{Type: html.ElementNode, Data: "span", DataAtom: atom.Span}
	at, seen := holder, make([]bool, len(u.marks)+1)
	rest := answer
	for {
		loc := mark.FindStringSubmatchIndex(rest)
		chunk := rest
		if loc != nil {
			chunk = rest[:loc[0]]
		}
		if chunk != "" {
			at.AppendChild(&html.Node{Type: html.TextNode, Data: chunk})
		}
		if loc == nil {
			break
		}
		closing, void := rest[loc[2]:loc[3]] == "/", rest[loc[6]:loc[7]] == "/"
		number, err := strconv.Atoi(rest[loc[4]:loc[5]])
		rest = rest[loc[1]:]
		if err != nil || number < 1 || number > len(u.marks) || (closing && void) {
			return "marks changed"
		}
		was := u.marks[number-1]
		switch {
		case closing:
			if at == holder || marked(at) != number {
				return "marks changed"
			}
			at = at.Parent
		case seen[number] || void != whole(was):
			return "marks changed"
		case void:
			seen[number] = true
			at.AppendChild(copyOf(was, true, number))
		default:
			seen[number] = true
			fresh := copyOf(was, false, number)
			at.AppendChild(fresh)
			at = fresh
		}
	}
	if at != holder {
		return "marks changed"
	}
	for number := 1; number <= len(u.marks); number++ {
		if !seen[number] {
			return "marks changed"
		}
	}
	unmark(holder)
	for holder.FirstChild != nil {
		n := holder.FirstChild
		holder.RemoveChild(n)
		u.parent.InsertBefore(n, u.nodes[0])
	}
	for _, n := range u.nodes {
		u.parent.RemoveChild(n)
	}
	return ""
}

// markKey is the attribute a copy carries while an answer is being read:
// the number of its mark.
const markKey = "\x00mark"

func copyOf(n *html.Node, deep bool, number int) *html.Node {
	fresh := &html.Node{Type: n.Type, Data: n.Data, DataAtom: n.DataAtom, Namespace: n.Namespace}
	fresh.Attr = append(append([]html.Attribute{}, n.Attr...), html.Attribute{Key: markKey, Val: strconv.Itoa(number)})
	if deep {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			fresh.AppendChild(clone(c))
		}
	}
	return fresh
}

func clone(n *html.Node) *html.Node {
	fresh := &html.Node{Type: n.Type, Data: n.Data, DataAtom: n.DataAtom, Namespace: n.Namespace, Attr: append([]html.Attribute{}, n.Attr...)}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		fresh.AppendChild(clone(c))
	}
	return fresh
}

func marked(n *html.Node) int {
	for _, a := range n.Attr {
		if a.Key == markKey {
			number, _ := strconv.Atoi(a.Val)
			return number
		}
	}
	return 0
}

func unmark(n *html.Node) {
	for i, a := range n.Attr {
		if a.Key == markKey {
			n.Attr = append(n.Attr[:i:i], n.Attr[i+1:]...)
			break
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		unmark(c)
	}
}

// same reports that an answer is the text it was asked to translate.
func (u *unit) same(answer string) bool {
	return len([]rune(u.plain)) >= 25 && strings.Join(strings.Fields(mark.ReplaceAllString(answer, "")), " ") == strings.Join(strings.Fields(mark.ReplaceAllString(u.marked, "")), " ")
}

// parse reads an HTML fragment as the content of a body.
func parse(fragment string) (*html.Node, error) {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragmentWithOptions(strings.NewReader(fragment), body, html.ParseOptionEnableScripting(false))
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}
	return body, nil
}

// render writes the content of a node.
func render(parent *html.Node) string {
	var b strings.Builder
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&b, c)
	}
	return b.String()
}

// verbatim reports whether the text of an element is not for translating.
func verbatim(n *html.Node) bool {
	return n.Type == html.ElementNode && (n.DataAtom == atom.Pre || n.DataAtom == atom.Code)
}

// whole reports whether an element inside a sentence goes to the model as
// one mark, content and all: code, and what has no words in it.
func whole(n *html.Node) bool {
	return verbatim(n) || n.FirstChild == nil || !letters.MatchString(text(n))
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
