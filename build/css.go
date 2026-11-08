package build

import (
	"bytes"
	"fmt"
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
var cssStringRE = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
var cssPreludeRE = regexp.MustCompile(`([^{};]*)\{`)
var cssClassRE = regexp.MustCompile(`\.(-?[A-Za-z_][A-Za-z0-9_-]*)`)
var cssAttrRE = regexp.MustCompile(`\[\s*([A-Za-z_][A-Za-z0-9_-]*)`)
var cssDefRE = regexp.MustCompile(`(?:^|[{;])\s*(--[A-Za-z0-9_-]+)\s*:`)
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
	lineAt := func(off int) string {
		return fmt.Sprintf("%s:%d", file, 1+bytes.Count(src[:off], []byte("\n")))
	}
	clean := cssCommentRE.ReplaceAllFunc(src, blank)
	for _, m := range cssURLRE.FindAllSubmatchIndex(clean, -1) {
		u := strings.TrimSpace(string(clean[m[2]:m[3]]))
		if !strings.HasPrefix(u, "data:") {
			c.urls[u] = lineAt(m[0])
		}
	}
	noStr := cssStringRE.ReplaceAllFunc(clean, func(b []byte) []byte {
		out := blank(b)
		out[0], out[len(out)-1] = '"', '"'
		return out
	})
	for _, m := range cssPreludeRE.FindAllSubmatchIndex(noStr, -1) {
		pre := noStr[m[2]:m[3]]
		if bytes.HasPrefix(bytes.TrimSpace(pre), []byte("@")) {
			continue
		}
		for _, cm := range cssClassRE.FindAllSubmatchIndex(pre, -1) {
			name := string(pre[cm[2]:cm[3]])
			if _, ok := c.classes[name]; !ok {
				c.classes[name] = lineAt(m[2] + cm[0])
			}
		}
		for _, am := range cssAttrRE.FindAllSubmatch(pre, -1) {
			c.attrs[strings.ToLower(string(am[1]))] = true
		}
	}
	for _, m := range cssDefRE.FindAllSubmatchIndex(noStr, -1) {
		name := string(noStr[m[2]:m[3]])
		if _, ok := c.varDefs[name]; !ok {
			c.varDefs[name] = lineAt(m[2])
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
