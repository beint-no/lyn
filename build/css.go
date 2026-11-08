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
	ids     map[string]string
	probs   []string
}

func newCSSInfo() *cssInfo {
	return &cssInfo{classes: map[string]string{}, varDefs: map[string]string{}, varUses: map[string]bool{}, varBare: map[string]string{}, attrs: map[string]bool{}, urls: map[string]string{}, ids: map[string]string{}}
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
	c.rules(noStr, clean, lineAt)
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

type cssBlock struct {
	at    string
	decls map[string]bool
}

func (c *cssInfo) rules(b, orig []byte, lineAt func(int) string) {
	var stack []cssBlock
	problem := func(off int, format string, args ...any) {
		c.probs = append(c.probs, lineAt(off)+": "+fmt.Sprintf(format, args...))
	}
	context := func() (string, bool) {
		for i, s := range slices.Backward(stack) {
			switch at := s.at; at {
			case "", "media", "supports", "container", "layer", "scope", "starting-style":
			case "font-feature-values":
				return at, i < len(stack)-1
			default:
				return at, false
			}
		}
		return "", false
	}
	seg := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case 'u':
			if bytes.HasPrefix(b[i:], []byte("url(")) {
				if end := bytes.IndexByte(b[i:], ')'); end > 0 {
					i += end
				}
			}
		case '{':
			pre := bytes.TrimSpace(b[seg:i])
			start := seg + bytes.Index(b[seg:i+1], pre)
			blk := cssBlock{}
			if len(pre) > 0 && pre[0] == '@' {
				blk.at = c.atRule(pre, start, problem)
			} else if outer, _ := context(); outer != "keyframes" && outer != "font-feature-values" {
				c.selector(pre, start, lineAt, problem)
			}
			stack = append(stack, blk)
			seg = i + 1
		case ';', '}':
			decl := bytes.TrimSpace(b[seg:i])
			start := seg + bytes.Index(b[seg:i+1], decl)
			if len(decl) > 0 && decl[0] == '@' {
				c.atRule(decl, start, problem)
			} else if len(decl) > 0 && len(stack) > 0 {
				c.declaration(decl, orig[start:start+len(decl)], start, &stack[len(stack)-1], context, problem)
			}
			if b[i] == '}' && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			seg = i + 1
		}
	}
}

func (c *cssInfo) atRule(pre []byte, off int, problem func(int, string, ...any)) string {
	n := 1
	for n < len(pre) && isPropertyByte(pre[n]) {
		n++
	}
	name := strings.ToLower(string(pre[1:n]))
	if !cssAtRules[name] && !strings.HasPrefix(name, "-") {
		problem(off, "unknown at-rule @%s%s; browsers drop the whole rule", name, suggest(name, keysOf(cssAtRules)))
	}
	return name
}

func (c *cssInfo) selector(pre []byte, off int, lineAt func(int) string, problem func(int, string, ...any)) {
	for i := 0; i < len(pre); i++ {
		switch pre[i] {
		case '\\':
			i++
		case '#':
			if i+1 >= len(pre) || pre[i+1] >= '0' && pre[i+1] <= '9' || !isPropertyByte(pre[i+1]) {
				continue
			}
			j := i + 1
			for j < len(pre) && isPropertyByte(pre[j]) {
				j++
			}
			name := string(pre[i+1 : j])
			if _, ok := c.ids[name]; !ok {
				c.ids[name] = lineAt(off + i)
			}
			i = j - 1
		case ':':
			elem := i+1 < len(pre) && pre[i+1] == ':'
			j := i + 1
			if elem {
				j++
			}
			k := j
			for k < len(pre) && isPropertyByte(pre[k]) {
				k++
			}
			name := strings.ToLower(string(pre[j:k]))
			known, kind, prefix := cssPseudoClasses, "pseudo-class :", ":"
			if elem {
				known, kind, prefix = cssPseudoElements, "pseudo-element ::", "::"
			}
			if name != "" && name[0] != '-' && !known[name] {
				problem(off+i, "unknown %s%s%s; browsers drop the whole rule", kind, name, suggestPrefixed(name, known, prefix))
			}
			i = k - 1
		}
	}
}

func suggestPrefixed(name string, known map[string]bool, prefix string) string {
	h := suggest(name, keysOf(known))
	if h == "" {
		return ""
	}
	return strings.Replace(h, `"`, `"`+prefix, 1)
}

func (c *cssInfo) declaration(decl, orig []byte, off int, blk *cssBlock, context func() (string, bool), problem func(int, string, ...any)) {
	colon := bytes.IndexByte(decl, ':')
	if colon <= 0 {
		problem(off, "%q is not a declaration; write property: value", string(decl))
		return
	}
	name := strings.ToLower(string(bytes.TrimSpace(decl[:colon])))
	if strings.HasPrefix(name, "-") {
		return
	}
	value := strings.Join(strings.Fields(string(orig[colon+1:])), " ")
	if value == "" {
		problem(off, "%s has no value; set one or remove the declaration", name)
		return
	}
	at, skip := context()
	if skip {
		return
	}
	known := cssProperties
	if d := cssDescriptors[at]; d != nil {
		known = d
	}
	if !known[name] {
		where := "property"
		if at != "" && cssDescriptors[at] != nil {
			where = "descriptor in @" + at
		}
		problem(off, "unknown CSS %s %s%s; browsers ignore the declaration", where, name, suggest(name, keysOf(known)))
		return
	}
	if kw := cssKeywords[name]; kw != nil && !strings.ContainsAny(value, "(") {
		for tok := range strings.FieldsSeq(strings.ToLower(strings.TrimSuffix(value, "!important"))) {
			if !kw[tok] && !cssGlobalValues[tok] {
				problem(off, "invalid value %s for %s%s; browsers ignore the declaration", tok, name, suggest(tok, keysOf(kw)))
				break
			}
		}
	}
	key := name + ":" + value
	if blk.decls == nil {
		blk.decls = map[string]bool{}
	}
	if blk.decls[key] {
		problem(off, "duplicate declaration %s: %s in the same rule; remove one", name, value)
	}
	blk.decls[key] = true
}
