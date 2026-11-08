package build

import (
	"bytes"
	"iter"
	"regexp"
	"strings"
)

type cssInfo struct {
	classes map[string]string
	varDefs map[string]string
	varUses map[string]bool
	varBare map[string]string
	attrs   map[string]bool
	urls    map[string]string
}

func newCSSInfo() *cssInfo {
	return &cssInfo{classes: map[string]string{}, varDefs: map[string]string{}, varUses: map[string]bool{}, varBare: map[string]string{}, attrs: map[string]bool{}, urls: map[string]string{}}
}

var cssCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
var cssClassRE = regexp.MustCompile(`\.(-?[A-Za-z_][A-Za-z0-9_-]*)`)
var cssAttrRE = regexp.MustCompile(`\[\s*([A-Za-z_][A-Za-z0-9_-]*)`)
var cssURLRE = regexp.MustCompile(`url\(\s*["']?([^"')]+)`)

func (c *cssInfo) analyze(file string, src []byte) error {
	clean := cssCommentRE.ReplaceAll(src, nil)
	for _, m := range cssURLRE.FindAllSubmatch(clean, -1) {
		u := strings.TrimSpace(string(m[1]))
		if !strings.HasPrefix(u, "data:") {
			c.urls[u] = file
		}
	}
	noStr := blankStrings(clean)
	for pre := range preludes(noStr) {
		if bytes.HasPrefix(bytes.TrimSpace(pre), []byte("@")) {
			continue
		}
		for _, cm := range cssClassRE.FindAllSubmatch(pre, -1) {
			name := string(cm[1])
			if _, ok := c.classes[name]; !ok {
				c.classes[name] = file
			}
		}
		for _, am := range cssAttrRE.FindAllSubmatch(pre, -1) {
			c.attrs[strings.ToLower(string(am[1]))] = true
		}
	}
	for name := range propertyDefs(noStr) {
		if _, ok := c.varDefs[string(name)]; !ok {
			c.varDefs[string(name)] = file
		}
	}
	c.scanVars(file, noStr)
	return nil
}

func (c *cssInfo) scanVars(file string, b []byte) {
	for _, m := range varUseRE.FindAllSubmatch(b, -1) {
		name := string(m[1])
		c.varUses[name] = true
		if string(m[2]) == ")" {
			if _, ok := c.varBare[name]; !ok {
				c.varBare[name] = file
			}
		}
	}
}

func blankStrings(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if q := b[i]; q == '"' || q == '\'' {
			if end := stringEnd(b, i); end > 0 {
				out = append(out, '"', '"')
				i = end
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
}

func stringEnd(b []byte, i int) int {
	q := b[i]
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case q:
			return j + 1
		case '\\':
			if j+1 >= len(b) || b[j+1] == '\n' {
				return -1
			}
			j++
		}
	}
	return -1
}

func preludes(b []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		start := 0
		for i, c := range b {
			switch c {
			case '{':
				if !yield(b[start:i]) {
					return
				}
				start = i + 1
			case '}', ';':
				start = i + 1
			}
		}
	}
}

func propertyDefs(b []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		try := func(q int) (name []byte, end int) {
			for q < len(b) && isCSSSpace(b[q]) {
				q++
			}
			if q+2 >= len(b) || b[q] != '-' || b[q+1] != '-' || !isPropertyByte(b[q+2]) {
				return nil, -1
			}
			n := q + 2
			for n < len(b) && isPropertyByte(b[n]) {
				n++
			}
			e := n
			for e < len(b) && isCSSSpace(b[e]) {
				e++
			}
			if e >= len(b) || b[e] != ':' {
				return nil, -1
			}
			return b[q:n], e + 1
		}
		for p := 0; p < len(b); {
			name, end := []byte(nil), -1
			if p == 0 {
				name, end = try(0)
			}
			if end < 0 && (b[p] == '{' || b[p] == ';') {
				name, end = try(p + 1)
			}
			if end < 0 {
				p++
				continue
			}
			if !yield(name) {
				return
			}
			p = end
		}
	}
}

func isCSSSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

func isPropertyByte(c byte) bool {
	return c == '-' || c == '_' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}
