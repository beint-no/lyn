package build

import (
	"bytes"
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

func (c *cssInfo) analyze(file string, src []byte) error {
	clean := cssCommentRE.ReplaceAll(src, nil)
	for _, m := range cssURLRE.FindAllSubmatch(clean, -1) {
		u := strings.TrimSpace(string(m[1]))
		if !strings.HasPrefix(u, "data:") {
			c.urls[u] = file
		}
	}
	noStr := cssStringRE.ReplaceAll(clean, []byte(`""`))
	for _, m := range cssPreludeRE.FindAllSubmatch(noStr, -1) {
		pre := m[1]
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
	for _, m := range cssDefRE.FindAllSubmatch(noStr, -1) {
		if _, ok := c.varDefs[string(m[1])]; !ok {
			c.varDefs[string(m[1])] = file
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
