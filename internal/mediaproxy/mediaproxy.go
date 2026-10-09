// Package mediaproxy lets the server hand out the images of entries from its
// own address: it writes the addresses that lead through the server, tells
// such an address from one somebody made up, and puts them into the text of
// an entry. The scheme is that of Miniflux.
package mediaproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Path is where the addresses that lead through the server start, after
// the public address of the server.
const Path = "/proxy/"

// Which images go through the server.
const (
	// ModeNone leaves every address as it is.
	ModeNone = "none"
	// ModeHTTPOnly takes the images served over http, which a page served
	// over https would not show.
	ModeHTTPOnly = "http-only"
	// ModeAll takes every image.
	ModeAll = "all"
)

// Modes are the modes there are, the default first.
var Modes = []string{ModeHTTPOnly, ModeAll, ModeNone}

// Key derives the key addresses are signed with from the secret of the
// installation.
func Key(salt string) []byte {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte("freshgo media proxy"))
	return mac.Sum(nil)
}

func sign(key []byte, target string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(target))
	return mac.Sum(nil)
}

// Address returns the address under base that hands out what is at target.
func Address(key []byte, base, target string) string {
	return base + Path + base64.RawURLEncoding.EncodeToString(sign(key, target)) + "/" + base64.RawURLEncoding.EncodeToString([]byte(target))
}

// Target returns what the two parts of an address after Path stand for,
// when the address was written with the key and leads to an http or https
// address.
func Target(key []byte, digest, encoded string) (string, bool) {
	signature, err := base64.RawURLEncoding.DecodeString(digest)
	if err != nil {
		return "", false
	}
	target, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !hmac.Equal(signature, sign(key, string(target))) {
		return "", false
	}
	return string(target), remote(string(target), ModeAll)
}

// remote reports whether the address is one the mode sends through the
// server.
func remote(address, mode string) bool {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return mode == ModeHTTPOnly || mode == ModeAll
	case "https":
		return mode == ModeAll
	}
	return false
}

// Rewrite returns the text of an entry with the addresses of its images
// replaced by what through gives for them: src and srcset of img, srcset of
// the sources of a picture, the poster of a video. A tag with a replaced
// address is written anew; the rest of the text, and a text none of whose
// addresses the mode takes, comes back as it is, byte for byte.
func Rewrite(content, mode string, through func(target string) string) string {
	if mode != ModeHTTPOnly && mode != ModeAll {
		return content
	}
	if !strings.Contains(content, "<img") && !strings.Contains(content, "<source") && !strings.Contains(content, "<video") {
		return content
	}
	if !taken(content, mode) {
		return content
	}
	changed := false
	one := func(address string) string {
		if !remote(address, mode) {
			return address
		}
		changed = true
		return through(strings.TrimSpace(address))
	}
	var out strings.Builder
	out.Grow(len(content) + len(content)/8)
	// The tokenizer reads what is inside a comment, a text area or a script
	// as text, so nothing there is taken for a tag.
	z := html.NewTokenizer(strings.NewReader(content))
	pictures := 0
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		raw := z.Raw()
		name := tagName(raw)
		switch {
		case kind == html.EndTagToken && name == "picture" && pictures > 0:
			pictures--
		case kind == html.StartTagToken && name == "picture":
			pictures++
		case (kind == html.StartTagToken || kind == html.SelfClosingTagToken) &&
			(name == "img" || name == "video" || name == "source" && pictures > 0):
			// Reading the attributes changes what Raw returned.
			asWritten := string(raw)
			tag := html.Token{Type: kind, Data: name}
			before := changed
			changed = false
			for _, more := z.TagName(); more; {
				var key, value []byte
				key, value, more = z.TagAttr()
				attr := html.Attribute{Key: string(key), Val: string(value)}
				switch {
				case attr.Key == "src" && name == "img", attr.Key == "poster" && name == "video":
					attr.Val = one(attr.Val)
				case attr.Key == "srcset" && name != "video":
					attr.Val = srcset(attr.Val, one)
				}
				tag.Attr = append(tag.Attr, attr)
			}
			if changed {
				out.WriteString(tag.String())
			} else {
				out.WriteString(asWritten)
			}
			changed = changed || before
			continue
		}
		out.Write(raw)
	}
	if !changed || z.Err() != io.EOF {
		return content
	}
	return out.String()
}

// tagName returns the name of the tag a token of the tokenizer starts
// with, in lower case; the parser reads <image> as <img>. It is "" for what
// is no tag. Asking the tokenizer for the name would change the token.
func tagName(raw []byte) string {
	if len(raw) < 2 || raw[0] != '<' {
		return ""
	}
	start := 1
	if raw[1] == '/' {
		start = 2
	}
	end := start
	for end < len(raw) && (raw[end]|0x20 >= 'a' && raw[end]|0x20 <= 'z') {
		end++
	}
	// The names that matter are short: no other is worth a string.
	if end-start < 3 || end-start > 7 {
		return ""
	}
	switch name := strings.ToLower(string(raw[start:end])); name {
	case "image":
		return "img"
	case "img", "video", "source", "picture":
		return name
	}
	return ""
}

// taken reports whether the text may have an address the mode takes. It
// reads the tags that can carry one without building a document, which
// costs many times less: most texts have no such address, and they need no
// document. It may say yes where the document then has nothing to replace,
// never the reverse.
func taken(content, mode string) bool {
	found := false
	one := func(address string) string {
		found = found || remote(address, mode)
		return address
	}
	for rest := content; ; {
		at := strings.IndexByte(rest, '<')
		if at < 0 || len(rest)-at < 4 {
			return false
		}
		rest = rest[at:]
		// The parser reads <image> as <img>.
		if head := rest[1:4]; !strings.EqualFold(head, "img") && !strings.EqualFold(head, "ima") &&
			!strings.EqualFold(head, "sou") && !strings.EqualFold(head, "vid") {
			rest = rest[1:]
			continue
		}
		z := html.NewTokenizer(strings.NewReader(rest))
		if kind := z.Next(); kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			_, more := z.TagName()
			for more {
				var key, value []byte
				key, value, more = z.TagAttr()
				switch string(key) {
				case "src", "poster":
					one(string(value))
				case "srcset":
					srcset(string(value), one)
				}
			}
			if found {
				return true
			}
		}
		rest = rest[1:]
	}
}

// srcset rewrites the addresses of a list of image candidates. A candidate
// is an address and, after white space, what says when to take it; a comma
// ends it.
func srcset(list string, one func(string) string) string {
	const space = " \t\n\f\r"
	var out []string
	for rest := list; ; {
		rest = strings.TrimLeft(rest, space+",")
		if rest == "" {
			break
		}
		end := strings.IndexAny(rest, space)
		if end < 0 {
			end = len(rest)
		}
		address, descriptor := rest[:end], ""
		rest = rest[end:]
		if trimmed := strings.TrimRight(address, ","); trimmed != address {
			// The comma that ends the candidate came right after the address.
			address = trimmed
		} else if comma := strings.IndexByte(rest, ','); comma >= 0 {
			descriptor, rest = strings.Trim(rest[:comma], space), rest[comma:]
		} else {
			descriptor, rest = strings.Trim(rest, space), ""
		}
		candidate := one(address)
		if descriptor != "" {
			candidate += " " + descriptor
		}
		out = append(out, candidate)
	}
	return strings.Join(out, ", ")
}
