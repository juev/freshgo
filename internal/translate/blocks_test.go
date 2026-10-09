package translate

import (
	"strings"
	"testing"
)

const article = `<div><h2>Hello</h2><p>See the <a href="https://example.org/c">catalogue of <b>feeds</b></a> at <code>curl example.org</code>, <i>dear</i> reader<sup><a href="#n1">[1]</a></sup>.</p>` +
	`<pre>Hello reader</pre><ul><li>Bye<ul><li>dear reader</li></ul></li><li>42</li></ul><img src="https://example.org/i.png" alt="x">First line<br><br>Second line</div>`

// The text of an entry is cut into its sentences, with numbered marks
// where the elements inside them stand.
func TestUnits(t *testing.T) {
	root, err := parse(article)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range units(root) {
		got = append(got, u.marked)
	}
	want := []string{"Hello", "See the <1>catalogue of <2>feeds</2></1> at <3/>, <4>dear</4> reader<5/>.", "Bye", "dear reader", "<1/>First line", "Second line"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("units:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// An answer is put into the markup by its marks, which may have moved,
// and refused when a mark is missing, doubled, changed or badly nested.
func TestPut(t *testing.T) {
	paragraph := func() (*unit, func() string) {
		root, err := parse(article)
		if err != nil {
			t.Fatal(err)
		}
		return units(root)[1], func() string { return render(root) }
	}
	u, page := paragraph()
	if problem := u.put("<4>Дорогой</4> читатель<5/>, смотрите <3/> и <1><2>лент</2> каталог</1>."); problem != "" {
		t.Fatalf("an answer with its marks moved: %s", problem)
	}
	want := `<p><i>Дорогой</i> читатель<sup><a href="#n1">[1]</a></sup>, смотрите <code>curl example.org</code> и <a href="https://example.org/c"><b>лент</b> каталог</a>.</p>`
	if got := page(); !strings.Contains(got, want) || strings.Contains(got, "mark") {
		t.Errorf("the paragraph after the answer:\n%s\nwant in it:\n%s", got, want)
	}
	for name, answer := range map[string]string{
		"nothing":               " ",
		"a mark missing":        "Смотрите <1>каталог <2>лент</2></1> в <3/>, <4>дорогой</4> читатель.",
		"a mark twice":          "Смотрите <1>каталог <2>лент</2></1> в <3/><3/>, <4>дорогой</4> читатель<5/>.",
		"a mark unknown":        "Смотрите <1>каталог <2>лент</2></1> в <3/>, <4>дорогой</4> читатель<5/><6/>.",
		"code opened as a pair": "Смотрите <1>каталог <2>лент</2></1> в <3>curl</3>, <4>дорогой</4> читатель<5/>.",
		"a pair made void":      "Смотрите <1>каталог <2>лент</2></1> в <3/>, <4/> читатель<5/>.",
		"marks crossed":         "Смотрите <1>каталог <2>лент</1></2> в <3/>, <4>дорогой</4> читатель<5/>.",
		"a mark left open":      "Смотрите <1>каталог <2>лент</2> в <3/>, <4>дорогой</4> читатель<5/>.",
	} {
		u, page := paragraph()
		before := page()
		if problem := u.put(answer); problem == "" || page() != before {
			t.Errorf("%s: problem %q, the page changed: %v", name, problem, page() != before)
		}
	}
}
