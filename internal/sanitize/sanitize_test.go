package sanitize

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The force-https policy of the oracle run: the default list of FreshRSS
// plus testdata/reference/oracle/force-https.txt, reduced to what the cases use.
func oracleHTTPS(u string) string {
	for _, domain := range []string{"youtube.com", "github.com", "example.net"} {
		rest, ok := strings.CutPrefix(u, "http://")
		if !ok {
			continue
		}
		host, _, _ := strings.Cut(rest, "/")
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return "https://" + rest
		}
	}
	return u
}

// Where freshgo knowingly differs from FreshRSS, by the input of the case.
var deviations = map[string]struct{ want, why string }{
	`<font color=red>Tom & Jerry &lt;b&gt; <b class="x" id=i onclick=a()>bold</b></font><!-- c --><script>alert(1)</script>`: {
		`Tom &amp; Jerry &lt;b&gt; <b data-sanitized-id="i" data-sanitized-class="x">bold</b>`,
		"FreshRSS encodes the text of a removed element twice",
	},
	`<unknown attr="1">u &amp; v &lt;w&gt;</unknown>`: {
		`u &amp; v &lt;w&gt;`,
		"FreshRSS encodes the text of a removed element twice",
	},
	`</div><p>after stray close</p>`: {
		`<p>after stray close</p>`,
		"FreshRSS loses everything after a stray </div>",
	},
	`<dl><dt>t</dt><dd>d</dd></dl><ruby>漢<rp>(</rp><rt>kan</rt><rp>)</rp></ruby><wbr><bdi>b</bdi><bdo dir="rtl">o</bdo>`: {
		`<dl><dt>t</dt><dd>d</dd></dl><ruby>漢<rp>(</rp><rt>kan</rt><rp>)</rp></ruby><wbr><bdi>b</bdi><bdo dir="rtl">o</bdo>`,
		"libxml does not know that wbr is a void element",
	},
	`<table border="1"><tr><td colspan="2">c</td><th scope="col">h</th></tr></table>`: {
		`<table border="1"><tbody><tr><td colspan="2">c</td><th scope="col">h</th></tr></tbody></table>`,
		"an HTML5 parser puts table rows into a tbody",
	},
	`<audio src="a.mp3"></audio><video poster="p.jpg"><source src="v.webm" type="video/webm"><track src="s.vtt" kind="subtitles"></video>`: {
		`<audio src="http://example.org/dir/a.mp3" controls="controls" preload="none"></audio><video poster="http://example.org/dir/p.jpg" controls="controls" preload="none"><source src="http://example.org/dir/v.webm" type="video/webm"><track src="http://example.org/dir/s.vtt" kind="subtitles"></video>`,
		"libxml does not know that track is a void element",
	},
}

// The cases and the expected output come from FreshRSS itself:
// testdata/reference/oracle/generate.sh.
func TestHTMLAgainstFreshRSS(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/reference/oracle/sanitize.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ HTML, Base, Out string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("only %d cases", len(cases))
	}
	used := 0
	for _, c := range cases {
		want := c.Out
		if d, ok := deviations[c.HTML]; ok {
			used++
			if d.want == c.Out {
				t.Errorf("%q: listed as a deviation (%s) but FreshRSS gives the same", c.HTML, d.why)
			}
			want = d.want
		}
		if got := HTML(c.HTML, c.Base, oracleHTTPS); got != want {
			t.Errorf("HTML(%q, %q)\n got %q\nwant %q", c.HTML, c.Base, got, want)
		}
	}
	if used != len(deviations) {
		t.Errorf("%d of %d deviations match a case", used, len(deviations))
	}
}

func TestHTMLIsSafe(t *testing.T) {
	for _, input := range []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href=" JaVaScRiPt:alert(1)">x</a>`,
		`<iframe src="javascript:alert(1)"></iframe>`,
		`<iframe><script>alert(1)</script></iframe>`,
		`<svg onload=alert(1)><script>alert(1)</script></svg>`,
		`<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
		`<style>@import "javascript:alert(1)"</style>`,
		`<p style="background:url(javascript:alert(1))">x</p>`,
		`<xmp><script>alert(1)</script></xmp>`,
		`<noframes><script>alert(1)</script></noframes>`,
		`<template><script>alert(1)</script></template>`,
		`<form action="javascript:alert(1)"><input type=submit></form>`,
		`<object data="javascript:alert(1)"></object><embed src="javascript:alert(1)">`,
		`<a href="vbscript:x" onclick="alert(1)" formaction="javascript:alert(1)">x</a>`,
		`<img src="x" srcset="javascript:alert(1)">`,
		`<!--><script>alert(1)</script>-->`,
	} {
		got := HTML(input, "http://example.org/", nil)
		lower := strings.ToLower(got)
		for _, bad := range []string{"<script", "onerror=", "onload=", "onclick=", "style=", "<style", "formaction", "srcset", "<object", "<embed", "<form", "<svg", "xlink"} {
			if strings.Contains(lower, bad) {
				t.Errorf("HTML(%q) = %q, contains %q", input, got, bad)
			}
		}
		for _, attr := range []string{`href="javascript:`, `src="javascript:`, `href="java`} {
			if strings.Contains(lower, attr) {
				t.Errorf("HTML(%q) = %q, keeps a script URL", input, got)
			}
		}
	}
}

// Markup hidden in an attribute value stays text.
func TestHTMLKeepsAttributeValuesInert(t *testing.T) {
	got := HTML(`<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`, "", nil)
	if want := `<p title="&lt;/noscript&gt;&lt;img src=x onerror=alert(1)&gt;"></p>`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = HTML("<a href=\"java\nscript:alert(1)\">x</a>", "http://example.org/", nil)
	if want := "<a href=\"unsafe:java\nscript:alert(1)\">x</a>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHTMLQuotesAttributes(t *testing.T) {
	for in, want := range map[string]string{
		`<p title="plain">x</p>`:                  `<p title="plain">x</p>`,
		`<p title='with "double"'>x</p>`:          `<p title='with "double"'>x</p>`,
		`<p title="with 'single'">x</p>`:          `<p title="with 'single'">x</p>`,
		`<p title="both &quot; and ' &lt;">x</p>`: `<p title="both &quot; and ' &lt;">x</p>`,
	} {
		if got := HTML(in, "", nil); got != want {
			t.Errorf("HTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestXHTML(t *testing.T) {
	for in, want := range map[string]string{
		`<div><p>XHTML <em>content</em> <img src="img/pic.png" alt="pic"></p></div>`: `<p>XHTML <em>content</em> <img src="http://example.org/img/pic.png" alt="pic"></p>`,
		`<div class="x">a</div><p>ignored</p>`:                                       `a`,
		`<p>no div</p><p>ignored</p>`:                                                `<p>no div</p>`,
		`<div><div>inner</div></div>`:                                                `<div>inner</div>`,
		``:                                                                           ``,
	} {
		if got := XHTML(in, "http://example.org/", nil); got != want {
			t.Errorf("XHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAbsolutize(t *testing.T) {
	const base = "http://example.org/dir/page?q=1#top"
	tests := []struct{ reference, base, want string }{
		{"other", base, "http://example.org/dir/other"},
		{"/root", base, "http://example.org/root"},
		{"../up/./x/../y", base, "http://example.org/up/y"},
		{"//cdn.example.com/a.png", base, "http://cdn.example.com/a.png"},
		{"?z=2", base, "http://example.org/dir/page?z=2"},
		{"#f", base, "http://example.org/dir/page?q=1#f"},
		{"", base, "http://example.org/dir/page?q=1"},
		{"  /sp ", base, "http://example.org/sp"},
		{"HTTP://EXAMPLE.org:80/%7euser/é x", base, "http://example.org/~user/%C3%A9%20x"},
		{"https://example.org:443/a%2fb?x=%3d", base, "https://example.org/a%2Fb?x=%3D"},
		{"https://example.org:8443", base, "https://example.org:8443/"},
		{"http://example.org/a/../b/./c", base, "http://example.org/b/c"},
		{"http://user:pass@example.org/p", base, "http://user:pass@example.org/p"},
		{"mailto:a@b.c", base, "mailto:a@b.c"},
		{"data:text/html,x", base, "data:text/html,x"},
		{"rel", "", "rel"},
		{"/abs", "", "/abs"},
		{"rel", "not a base", "rel"},
		{"http://example.org/a?x=1&y=2", "", "http://example.org/a?x=1&y=2"},
	}
	for _, tt := range tests {
		got, ok := Absolutize(tt.reference, tt.base)
		if !ok || got != tt.want {
			t.Errorf("Absolutize(%q, %q) = %q, %v; want %q", tt.reference, tt.base, got, ok, tt.want)
		}
	}
	if got, ok := Absolutize("http://[::1", base); ok {
		t.Errorf("malformed reference resolved to %q", got)
	}
}

func TestAllowedScheme(t *testing.T) {
	for u, want := range map[string]bool{
		"http://example.org/":  true,
		"relative/path":        true,
		"mailto:a@b.c":         true,
		"data:text/html,x":     true,
		"javascript:alert(1)":  false,
		"JavaScript:alert(1)":  false,
		"java script:alert(1)": false,
		"java\nscript:alert":   false,
		":nothing":             false,
		"a.b:c":                false,
	} {
		if got := AllowedScheme(u); got != want {
			t.Errorf("AllowedScheme(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestText(t *testing.T) {
	for _, c := range []struct {
		fragment string
		limit    int
		want     string
	}{
		{"", 10, ""},
		{"<p>One</p><p>two&nbsp;&amp; three</p>", 50, "One two & three"},
		{"  <b>Bold</b>and plain\n\n text <img src=x> end ", 50, "Bold and plain text end"},
		{"<style>p{}</style>Shown<script>alert(1)</script> too", 50, "Shown too"},
		{"<p>Привет, мир и все остальные</p>", 12, "Привет, мир…"},
		{"exactly ten", 11, "exactly ten"},
		{"a &lt;b&gt; c", 50, "a <b> c"},
	} {
		if got := Text(c.fragment, c.limit); got != c.want {
			t.Errorf("Text(%q, %d) = %q, want %q", c.fragment, c.limit, got, c.want)
		}
	}
}

// A text stored with comments that mean something keeps them, and is clean
// around them.
func TestKeeping(t *testing.T) {
	const start, end = "<!-- FULLCONTENT start //-->", "<!-- FULLCONTENT end //-->"
	for _, tc := range []struct{ name, in, want string }{
		{"no comment to keep", `<p onclick="x()">a</p><!-- other -->`, `<p>a</p>`},
		{"a text between the two", start + `<p>page<script>x()</script></p>` + end + `<p>feed <a href="/b">b</a></p>`,
			start + `<p>page</p>` + end + `<p>feed <a href="https://e.example/b">b</a></p>`},
		{"other comments go", `<!-- a -->` + start + `<!-- b --><p>c</p>` + end, start + `<p>c</p>` + end},
		{"a comment twice", start + `a` + start + `b`, start + `a` + start + `b`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Keeping(tc.in, "https://e.example/a", start, end)
			if got != tc.want {
				t.Errorf("Keeping:\n got %s\nwant %s", got, tc.want)
			}
			if again := Keeping(got, "https://e.example/a", start, end); again != got {
				t.Errorf("a second cleaning changed the text:\n got %s\nwant %s", again, got)
			}
		})
	}
}

// The texts of an entry that came from elsewhere are cleaned, the marks of
// a full text stay, and what is clean is left as it is, byte for byte.
func TestStored(t *testing.T) {
	const base = "https://e.example/a"
	content := FullContentStart + `<p onclick="x()">page</p>` + FullContentEnd + `<p>feed<script>x()</script></p>`
	attributes := []byte(`{"original_content":"<p>feed<script>x()</script></p>","enclosures":[{"url":"https://e.example/a.mp3"}]}`)
	content, attributes, changed := Stored(content, attributes, base)
	if !changed {
		t.Fatal("Stored reports no change of an unclean entry")
	}
	if want := FullContentStart + `<p>page</p>` + FullContentEnd + `<p>feed</p>`; content != want {
		t.Errorf("content:\n got %s\nwant %s", content, want)
	}
	var attrs map[string]json.RawMessage
	if err := json.Unmarshal(attributes, &attrs); err != nil {
		t.Fatal(err)
	}
	if string(attrs["original_content"]) != `"\u003cp\u003efeed\u003c/p\u003e"` || string(attrs["enclosures"]) != `[{"url":"https://e.example/a.mp3"}]` {
		t.Errorf("attributes: %s", attributes)
	}
	if again, same, changed := Stored(content, attributes, base); changed || again != content || string(same) != string(attributes) {
		t.Errorf("a clean entry was changed: %s %s", again, same)
	}

	// Nothing to clean: not a byte changes, whatever the attributes are.
	for _, tc := range []struct{ content, attributes string }{
		{`Text with &lt;brackets&gt; &amp; &quot;quotes&quot;.`, `{"enclosures":[]}`},
		{`plain`, ``},
		{`plain`, `[1,2]`},
		{`<p>clean</p>`, `{"original_content":7}`},
	} {
		if got, attrs, changed := Stored(tc.content, []byte(tc.attributes), base); changed || got != tc.content || string(attrs) != tc.attributes {
			t.Errorf("Stored(%q, %q) = %q, %q, %v; want them as they are", tc.content, tc.attributes, got, attrs, changed)
		}
	}
}
