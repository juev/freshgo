// Package mediaproxy lets the server hand out the images of entries from its
// own address: it writes the addresses that lead through the server, tells
// such an address from one somebody made up, and puts them into the text of
// an entry. The scheme is that of Miniflux.
package mediaproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
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
// the sources of a picture, the poster of a video. A text none of whose
// addresses the mode takes comes back as it is, byte for byte.
func Rewrite(content, mode string, through func(target string) string) string {
	if mode != ModeHTTPOnly && mode != ModeAll {
		return content
	}
	if !strings.Contains(content, "<img") && !strings.Contains(content, "<source") && !strings.Contains(content, "<video") {
		return content
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<html><body>" + content + "</body></html>"))
	if err != nil {
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
	doc.Find("img[src], video[poster]").Each(func(_ int, node *goquery.Selection) {
		name := "src"
		if goquery.NodeName(node) == "video" {
			name = "poster"
		}
		node.SetAttr(name, one(node.AttrOr(name, "")))
	})
	doc.Find("img[srcset], picture source[srcset]").Each(func(_ int, node *goquery.Selection) {
		node.SetAttr("srcset", srcset(node.AttrOr("srcset", ""), one))
	})
	if !changed {
		return content
	}
	out, err := doc.Find("body").Html()
	if err != nil {
		return content
	}
	return out
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
