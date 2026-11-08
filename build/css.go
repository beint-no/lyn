package build

import (
	"bytes"
	"fmt"
	"iter"
	"regexp"
	"slices"
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

func blank(b []byte) []byte {
	out := bytes.Clone(b)
	for i, ch := range out {
		if ch != '\n' {
			out[i] = ' '
		}
	}
	return out
}

func (c *cssInfo) analyze(file string, src []byte) error {
	var newlines []int
	for i, ch := range src {
		if ch == '\n' {
			newlines = append(newlines, i)
		}
	}
	lineAt := func(off int) string {
		n, _ := slices.BinarySearch(newlines, off)
		return fmt.Sprintf("%s:%d", file, 1+n)
	}
	clean := cssCommentRE.ReplaceAllFunc(src, blank)
	for _, m := range cssURLRE.FindAllSubmatchIndex(clean, -1) {
		u := strings.TrimSpace(string(clean[m[2]:m[3]]))
		if !strings.HasPrefix(u, "data:") {
			c.urls[u] = lineAt(m[0])
		}
	}
	noStr := blankStrings(clean)
	for start, pre := range preludes(noStr) {
		if bytes.HasPrefix(bytes.TrimSpace(pre), []byte("@")) {
			continue
		}
		for _, cm := range cssClassRE.FindAllSubmatchIndex(pre, -1) {
			name := string(pre[cm[2]:cm[3]])
			if _, ok := c.classes[name]; !ok {
				c.classes[name] = lineAt(start + cm[0])
			}
		}
		for _, am := range cssAttrRE.FindAllSubmatch(pre, -1) {
			c.attrs[strings.ToLower(string(am[1]))] = true
		}
	}
	for off, def := range propertyDefs(noStr) {
		name := string(def)
		if _, ok := c.varDefs[name]; !ok {
			c.varDefs[name] = lineAt(off)
		}
	}
	for _, m := range varUseRE.FindAllSubmatchIndex(noStr, -1) {
		name := string(noStr[m[2]:m[3]])
		c.varUses[name] = true
		if noStr[m[4]] == ')' {
			if _, ok := c.varBare[name]; !ok {
				c.varBare[name] = lineAt(m[0])
			}
		}
	}
	return nil
}

func blankStrings(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if q := b[i]; q == '"' || q == '\'' {
			if end := stringEnd(b, i); end > 0 {
				out = append(out, '"')
				for _, ch := range b[i+1 : end-1] {
					if ch == '\n' {
						out = append(out, '\n')
					} else {
						out = append(out, ' ')
					}
				}
				out = append(out, '"')
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

func preludes(b []byte) iter.Seq2[int, []byte] {
	return func(yield func(int, []byte) bool) {
		start := 0
		for i, c := range b {
			switch c {
			case '{':
				if !yield(start, b[start:i]) {
					return
				}
				start = i + 1
			case '}', ';':
				start = i + 1
			}
		}
	}
}

func propertyDefs(b []byte) iter.Seq2[int, []byte] {
	return func(yield func(int, []byte) bool) {
		try := func(q int) (start, end int) {
			for q < len(b) && isCSSSpace(b[q]) {
				q++
			}
			if q+2 >= len(b) || b[q] != '-' || b[q+1] != '-' || !isPropertyByte(b[q+2]) {
				return -1, -1
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
				return -1, -1
			}
			return q, n
		}
		for p := 0; p < len(b); {
			start, end := -1, -1
			if p == 0 {
				start, end = try(0)
			}
			if start < 0 && (b[p] == '{' || b[p] == ';') {
				start, end = try(p + 1)
			}
			if start < 0 {
				p++
				continue
			}
			if !yield(start, b[start:end]) {
				return
			}
			p = bytes.IndexByte(b[end:], ':') + end + 1
		}
	}
}

func isCSSSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

func isPropertyByte(c byte) bool {
	return c == '-' || c == '_' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}
