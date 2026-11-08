package build

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var headerNameRE = regexp.MustCompile(`^!?\s*[A-Za-z0-9-]+$`)
var placeholderRE = regexp.MustCompile(`:[A-Za-z]\w*`)

func (p *Project) checkHeaders(files map[string]bool) {
	b, err := os.ReadFile(filepath.Join(p.Root, "static", "_headers"))
	if err != nil {
		return
	}
	var urls []string
	for f := range files {
		urls = append(urls, "/"+f)
		if f == "index.html" || strings.HasSuffix(f, "/index.html") {
			urls = append(urls, "/"+strings.TrimSuffix(f, "index.html"))
		}
	}
	seen := map[string]int{}
	rule, ruleLine, headers := "", 0, 0
	closeRule := func() {
		if rule != "" && headers == 0 {
			p.errs.add("static/_headers:%d: rule %s sets no headers; add indented \"Name: value\" lines or remove it", ruleLine, rule)
		}
	}
	for n, line := range strings.Split(string(b), "\n") {
		n++
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			continue
		case strings.HasPrefix(trim, "#"):
			p.errs.add("static/_headers:%d: comments are not allowed", n)
		case line[0] == ' ' || line[0] == '\t':
			if rule == "" {
				p.errs.add("static/_headers:%d: header line before any URL rule; start with a path such as /*", n)
				continue
			}
			headers++
			if strings.HasPrefix(trim, "!") {
				if !headerNameRE.MatchString(trim) {
					p.errs.add("static/_headers:%d: expected \"! Header-Name\" to detach a header", n)
				}
				continue
			}
			name, value, ok := strings.Cut(trim, ":")
			if !ok || !headerNameRE.MatchString(name) || strings.HasPrefix(name, "!") {
				p.errs.add("static/_headers:%d: expected an indented \"Header-Name: value\" line", n)
			} else if strings.TrimSpace(value) == "" {
				p.errs.add("static/_headers:%d: header %s has no value", n, name)
			}
		default:
			closeRule()
			rule, ruleLine, headers = trim, n, 0
			if prev, ok := seen[trim]; ok {
				p.errs.add("static/_headers:%d: rule %s is already defined on line %d; merge them", n, trim, prev)
			}
			seen[trim] = n
			pattern := trim
			if rest, ok := strings.CutPrefix(trim, p.BaseURL); ok && strings.HasPrefix(rest, "/") {
				pattern = rest
			} else if strings.HasPrefix(trim, "https://") {
				continue
			} else if !strings.HasPrefix(trim, "/") {
				p.errs.add("static/_headers:%d: URL rule %q must start with / or https://", n, trim)
				continue
			}
			if strings.HasPrefix(pattern, "/"+assetDir) {
				p.errs.add("static/_headers:%d: lyn already caches /%s* immutably; remove this rule", n, assetDir)
				continue
			}
			re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
			re = placeholderRE.ReplaceAllString(re, "[^/]+")
			rx, err := regexp.Compile(re)
			if err != nil {
				p.errs.add("static/_headers:%d: invalid URL rule %s", n, trim)
				continue
			}
			matched := false
			for _, u := range urls {
				if rx.MatchString(u) {
					matched = true
					break
				}
			}
			if !matched {
				p.errs.add("static/_headers:%d: rule %s matches no file in the build; fix the path or remove the rule", n, trim)
			}
		}
	}
	closeRule()
}

const assetDir = "_assets/"

const assetHeaders = "/_assets/*\n  Cache-Control: public, max-age=31536000, immutable\n"

func (p *Project) headers() []byte {
	b, _ := os.ReadFile(filepath.Join(p.Root, "static", "_headers"))
	b = bytes.TrimRight(b, "\n")
	if len(b) > 0 {
		b = append(b, "\n\n"...)
	}
	return append(b, assetHeaders...)
}
