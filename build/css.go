package build

import (
	"bytes"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
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

func (c *cssInfo) analyze(file string, src []byte) error {
	p := css.NewParser(parse.NewInput(bytes.NewReader(src)), false)
	for {
		gt, _, data := p.Next()
		switch gt {
		case css.ErrorGrammar:
			if p.Err() != nil && p.Err().Error() != "EOF" {
				return p.Err()
			}
			return nil
		case css.BeginRulesetGrammar, css.QualifiedRuleGrammar:
			vals := p.Values()
			for i, v := range vals {
				switch {
				case v.TokenType == css.DelimToken && string(v.Data) == "." && i+1 < len(vals) && vals[i+1].TokenType == css.IdentToken:
					name := string(vals[i+1].Data)
					if _, ok := c.classes[name]; !ok {
						c.classes[name] = file
					}
				case v.TokenType == css.LeftBracketToken && i+1 < len(vals) && vals[i+1].TokenType == css.IdentToken:
					c.attrs[strings.ToLower(string(vals[i+1].Data))] = true
				}
			}
		case css.CustomPropertyGrammar:
			name := string(data)
			if _, ok := c.varDefs[name]; !ok {
				c.varDefs[name] = file
			}
			for _, v := range p.Values() {
				c.scanVars(file, v.Data)
			}
		case css.DeclarationGrammar, css.BeginAtRuleGrammar, css.AtRuleGrammar:
			var buf bytes.Buffer
			for _, v := range p.Values() {
				if v.TokenType == css.URLToken {
					u := strings.TrimSuffix(strings.TrimPrefix(string(v.Data), "url("), ")")
					u = strings.Trim(u, `"' `)
					if !strings.HasPrefix(u, "data:") {
						c.urls[u] = file
					}
				}
				buf.Write(v.Data)
			}
			c.scanVars(file, buf.Bytes())
		}
	}
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
