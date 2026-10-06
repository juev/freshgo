package importer

import (
	"fmt"
	"strconv"
	"strings"
)

// parsePHPConfig reads a FreshRSS configuration file: "<?php return <value>;"
// where the value is built from literals only. FreshRSS writes these files
// with var_export, so no expressions occur; the short array syntax, comments
// and double-quoted strings are accepted because the files are also edited by
// hand.
//
// A PHP array becomes a []any when its keys are 0, 1, 2, … in order (an empty
// array included) and a map[string]any otherwise.
func parsePHPConfig(src string) (any, error) {
	p := &phpParser{src: src}
	p.skipSpace()
	if !p.eat("<?php") {
		return nil, p.errorf("want <?php at the start")
	}
	p.skipSpace()
	if !p.eatWord("return") {
		return nil, p.errorf("want return")
	}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if !p.eat(";") {
		return nil, p.errorf("want ; after the value")
	}
	p.skipSpace()
	p.eat("?>")
	p.skipSpace()
	if p.pos != len(p.src) {
		return nil, p.errorf("unexpected text after the value")
	}
	return v, nil
}

type phpParser struct {
	src string
	pos int
}

func (p *phpParser) errorf(format string, args ...any) error {
	line := 1 + strings.Count(p.src[:p.pos], "\n")
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

func (p *phpParser) skipSpace() {
	for p.pos < len(p.src) {
		rest := p.src[p.pos:]
		switch {
		case rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r':
			p.pos++
		case rest[0] == '#' || strings.HasPrefix(rest, "//"):
			end := strings.IndexByte(rest, '\n')
			if end < 0 {
				end = len(rest)
			}
			p.pos += end
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest, "*/")
			if end < 0 {
				p.pos = len(p.src)
				return
			}
			p.pos += end + 2
		default:
			return
		}
	}
}

func (p *phpParser) eat(token string) bool {
	if strings.HasPrefix(p.src[p.pos:], token) {
		p.pos += len(token)
		return true
	}
	return false
}

// eatWord consumes a keyword, case-insensitively, when it is not a prefix of
// a longer word.
func (p *phpParser) eatWord(word string) bool {
	rest := p.src[p.pos:]
	if len(rest) < len(word) || !strings.EqualFold(rest[:len(word)], word) {
		return false
	}
	if len(rest) > len(word) && isWordByte(rest[len(word)]) {
		return false
	}
	p.pos += len(word)
	return true
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (p *phpParser) value() (any, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, p.errorf("unexpected end of file")
	}
	switch c := p.src[p.pos]; {
	case c == '\'':
		return p.singleQuoted()
	case c == '"':
		return p.doubleQuoted()
	case c == '[':
		p.pos++
		return p.array(']')
	case c == '-' || c == '+' || c == '.' || c >= '0' && c <= '9':
		return p.number()
	}
	switch {
	case p.eatWord("true"):
		return true, nil
	case p.eatWord("false"):
		return false, nil
	case p.eatWord("null"):
		return nil, nil
	case p.eatWord("array"):
		p.skipSpace()
		if !p.eat("(") {
			return nil, p.errorf("want ( after array")
		}
		return p.array(')')
	}
	return nil, p.errorf("unsupported expression: only literals and arrays are allowed")
}

// singleQuoted reads a '…' string, where only \\ and \' are escapes.
func (p *phpParser) singleQuoted() (string, error) {
	var b strings.Builder
	for i := p.pos + 1; i < len(p.src); i++ {
		c := p.src[i]
		switch {
		case c == '\\' && i+1 < len(p.src) && (p.src[i+1] == '\\' || p.src[i+1] == '\''):
			b.WriteByte(p.src[i+1])
			i++
		case c == '\'':
			p.pos = i + 1
			return b.String(), nil
		default:
			b.WriteByte(c)
		}
	}
	return "", p.errorf("unterminated string")
}

// doubleQuoted reads a "…" string with the common escapes. Variable
// interpolation is an expression and is refused.
func (p *phpParser) doubleQuoted() (string, error) {
	var b strings.Builder
	for i := p.pos + 1; i < len(p.src); i++ {
		c := p.src[i]
		switch {
		case c == '\\' && i+1 < len(p.src):
			i++
			switch e := p.src[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '"', '$':
				b.WriteByte(e)
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		case c == '$':
			p.pos = i
			return "", p.errorf("variables in strings are not supported")
		case c == '"':
			p.pos = i + 1
			return b.String(), nil
		default:
			b.WriteByte(c)
		}
	}
	return "", p.errorf("unterminated string")
}

func (p *phpParser) number() (any, error) {
	start := p.pos
	for p.pos < len(p.src) && strings.IndexByte("+-.0123456789eE", p.src[p.pos]) >= 0 {
		p.pos++
	}
	text := p.src[start:p.pos]
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		p.pos = start
		return nil, p.errorf("bad number %q", text)
	}
	return f, nil
}

// array reads the elements up to the closing bracket; the opening one has
// been consumed.
func (p *phpParser) array(closing byte) (any, error) {
	type element struct {
		key   string
		value any
	}
	var (
		elements []element
		isList   = true
		nextKey  int64
	)
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, p.errorf("unterminated array")
		}
		if p.src[p.pos] == closing {
			p.pos++
			break
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		key := strconv.FormatInt(nextKey, 10)
		p.skipSpace()
		if p.eat("=>") {
			switch k := v.(type) {
			case string:
				key = k
			case int64:
				key = strconv.FormatInt(k, 10)
			default:
				return nil, p.errorf("array key must be a string or an integer")
			}
			if v, err = p.value(); err != nil {
				return nil, err
			}
		}
		// PHP continues automatic keys after the largest integer key so far.
		if n, err := strconv.ParseInt(key, 10, 64); err == nil && n >= nextKey {
			nextKey = n + 1
		}
		if key != strconv.Itoa(len(elements)) {
			isList = false
		}
		elements = append(elements, element{key, v})
		p.skipSpace()
		if p.eat(",") {
			continue
		}
		p.skipSpace()
		if p.pos < len(p.src) && p.src[p.pos] == closing {
			p.pos++
			break
		}
		return nil, p.errorf("want , or the end of the array")
	}
	if isList {
		list := make([]any, len(elements))
		for i, e := range elements {
			list[i] = e.value
		}
		return list, nil
	}
	m := make(map[string]any, len(elements))
	for _, e := range elements {
		m[e.key] = e.value
	}
	return m, nil
}
