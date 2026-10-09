package mediaproxy

import (
	"strings"
	"testing"
)

func TestAddresses(t *testing.T) {
	key, other := Key("the salt"), Key("another salt")
	const target = "http://images.example/a b.png?x=1&y=2"
	address := Address(key, "https://reader.example/rss", target)
	rest, ok := strings.CutPrefix(address, "https://reader.example/rss/proxy/")
	digest, encoded, two := strings.Cut(rest, "/")
	if !ok || !two || strings.ContainsAny(digest+encoded, "/+=?&") {
		t.Fatalf("Address = %q", address)
	}
	if got, ok := Target(key, digest, encoded); !ok || got != target {
		t.Errorf("Target of an address = %q, %v", got, ok)
	}
	for name, parts := range map[string][2]string{
		"another key":        {strings.TrimPrefix(Address(other, "", target), Path)[:len(digest)], encoded},
		"another address":    {digest, strings.SplitN(strings.TrimPrefix(Address(key, "", target+"x"), Path), "/", 2)[1]},
		"no signature":       {"", encoded},
		"not base64":         {"!!", encoded},
		"an address cut off": {digest, encoded[:len(encoded)-2]},
	} {
		if got, ok := Target(key, parts[0], parts[1]); ok {
			t.Errorf("%s: Target = %q, want a refusal", name, got)
		}
	}
	// A signature does not make an address of another kind fetchable.
	for _, target := range []string{"file:///etc/passwd", "ftp://files.example/a.png", "//images.example/a.png", "javascript:alert(1)", ""} {
		parts := strings.SplitN(strings.TrimPrefix(Address(key, "", target), Path), "/", 2)
		if got, ok := Target(key, parts[0], parts[1]); ok {
			t.Errorf("Target of the signed %q = %q, want a refusal", target, got)
		}
	}
}

func TestRewrite(t *testing.T) {
	through := func(target string) string { return "/p/" + strings.NewReplacer("://", "-", "/", "-").Replace(target) }
	for _, tc := range []struct {
		name, mode, content, want string
	}{
		{"an http image", ModeHTTPOnly, `<p>a <img src="http://i.example/a.png" alt="x"> b</p>`, `<p>a <img src="/p/http-i.example-a.png" alt="x"/> b</p>`},
		{"an https image stays", ModeHTTPOnly, `<p><img src="https://i.example/a.png"></p>`, `<p><img src="https://i.example/a.png"></p>`},
		{"every image", ModeAll, `<img src="https://i.example/a.png"><img src="HTTP://i.example/b.png">`, `<img src="/p/https-i.example-a.png"/><img src="/p/HTTP-i.example-b.png"/>`},
		{"nothing", ModeNone, `<img src="http://i.example/a.png">`, `<img src="http://i.example/a.png">`},
		{"an unknown mode", "", `<img src="http://i.example/a.png">`, `<img src="http://i.example/a.png">`},
		{"what is not on another site", ModeAll, `<img src="/a.png"><img src="data:image/png;base64,AAAA"><img src="a.png"><img>`, `<img src="/a.png"><img src="data:image/png;base64,AAAA"><img src="a.png"><img>`},
		{"srcset", ModeAll, `<img src="https://i.example/a.png" srcset="https://i.example/a.png 1x,  https://i.example/b.png 2x">`,
			`<img src="/p/https-i.example-a.png" srcset="/p/https-i.example-a.png 1x, /p/https-i.example-b.png 2x"/>`},
		{"srcset with widths and a comma in an address", ModeAll, `<img srcset="https://i.example/a,b.png 480w, https://i.example/c.png">`,
			`<img srcset="/p/https-i.example-a,b.png 480w, /p/https-i.example-c.png"/>`},
		{"srcset where only some are taken", ModeHTTPOnly, `<img srcset="https://i.example/a.png 1x, http://i.example/b.png 2x">`,
			`<img srcset="https://i.example/a.png 1x, /p/http-i.example-b.png 2x"/>`},
		{"a picture", ModeAll, `<picture><source srcset="https://i.example/a.webp" type="image/webp"><img src="https://i.example/a.png"></picture>`,
			`<picture><source srcset="/p/https-i.example-a.webp" type="image/webp"/><img src="/p/https-i.example-a.png"/></picture>`},
		{"the poster of a video, not the video", ModeAll, `<video poster="https://i.example/p.png" src="https://v.example/v.mp4"></video>`,
			`<video poster="/p/https-i.example-p.png" src="https://v.example/v.mp4"></video>`},
		{"the source of an audio stays", ModeAll, `<audio><source src="https://a.example/a.mp3"></audio> &amp; text`, `<audio><source src="https://a.example/a.mp3"></audio> &amp; text`},
		{"a link stays", ModeAll, `<a href="http://i.example/a.png">a.png</a>`, `<a href="http://i.example/a.png">a.png</a>`},
		{"an address written with a character reference", ModeHTTPOnly, `<img src="http&#58;//i.example/a.png">`, `<img src="/p/http-i.example-a.png"/>`},
		{"an image after one that stays", ModeHTTPOnly, `<img src="https://i.example/a.png"><p>b</p><img alt="x" src=" http://i.example/b.png ">`,
			`<img src="https://i.example/a.png"/><p>b</p><img alt="x" src="/p/http-i.example-b.png"/>`},
		{"a tag in capitals", ModeHTTPOnly, `<img src="https://i.example/a.png"><IMG SRC="http://i.example/b.png">`, `<img src="https://i.example/a.png"/><img src="/p/http-i.example-b.png"/>`},
		{"what looks like an image inside a text area", ModeAll, `<textarea><img src="https://i.example/a.png"></textarea>`, `<textarea><img src="https://i.example/a.png"></textarea>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Rewrite(tc.content, tc.mode, through); got != tc.want {
				t.Errorf("Rewrite:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A text with images none of which the mode takes, the usual case with
// http-only: about 6 KB and three images over https. And the same text with
// the images over http, which has to be written anew.
func BenchmarkRewrite(b *testing.B) {
	text := func(scheme string) string {
		paragraphs := strings.Repeat(`<p>Сегодня вышла новая версия, <a href="https://example.org/a?x=1&amp;y=2">подробности</a> — "в статье". The quick brown fox jumps over the lazy dog.</p>`+"\n", 10)
		return strings.Repeat(paragraphs+`<p><img src="`+scheme+`://example.org/i.png" alt="x" width="600"/></p>`+"\n", 3)
	}
	through := func(target string) string { return "/p/" + target }
	for _, tc := range []struct{ name, content string }{
		{"nothing to replace", text("https")},
		{"three images to replace", text("http")},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Rewrite(tc.content, ModeHTTPOnly, through)
			}
		})
	}
}
