package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type Scan struct {
	Links   []string
	IDs     []string
	Classes []string
	Data    []string
	VarDefs []string
	VarUses []string
	VarBare []string
	Words   []string
	Title   string
	Titles  int
	Desc    string
	NoIndex bool
	Errs    []ScanErr
	Imgs    int
	H1      int
	H1Off   int
	Canon   []string
	OGURL   string
	Alts    []string
}

type ScanErr struct {
	Off   int
	Msg   string
	Words []string
}

var varUseRE = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)\s*([,)])`)
var varDefRE = regexp.MustCompile(`(?:^|;)\s*(--[A-Za-z0-9_-]+)\s*:`)

func scanHTML(b []byte) Scan {
	var s Scan
	var stack []string
	var opened []int
	var needName []nameReq
	var labels []nameReq
	codeDepth, svgDepth, tplDepth := 0, 0, 0
	s.H1Off = -1
	seenIDs := map[string]bool{}
	errf := func(off int, words []string, format string, args ...any) {
		if len(s.Errs) < 20 {
			s.Errs = append(s.Errs, ScanErr{off, fmt.Sprintf(format, args...), words})
		}
	}
	textStart := 0
	checkText := func(start int, t []byte) {
		if len(needName) > 0 && len(bytes.TrimSpace(t)) > 0 {
			for k := range needName {
				needName[k].ok = true
			}
		}
		if codeDepth > 0 || len(t) == 0 {
			return
		}
		for _, pat := range []string{"{{", "}}", "](", "**", "LYNSC", "$$", "\\text{", "\\frac{"} {
			if i := bytes.Index(t, []byte(pat)); i >= 0 {
				snippet := strings.ToValidUTF8(strings.TrimSpace(string(t[max(0, i-30):min(len(t), i+30)])), "")
				errf(start+i, []string{snippet, html.UnescapeString(snippet), pat}, "visible markup %q in page text: %q; the syntax was not rendered, fix or escape it", pat, snippet)
				return
			}
		}
	}
	i := 0
	for i < len(b) {
		if b[i] != '<' {
			i++
			continue
		}
		checkText(textStart, b[textStart:i])
		switch {
		case bytes.HasPrefix(b[i:], []byte("<!--")):
			end := bytes.Index(b[i:], []byte("-->"))
			if end < 0 {
				errf(i, []string{"<!--"}, "HTML comment is never closed; add -->")
				return s
			}
			i += end + 3
			textStart = i
			continue
		case bytes.HasPrefix(b[i:], []byte("<!")):
			end := bytes.IndexByte(b[i:], '>')
			if end < 0 {
				return s
			}
			i += end + 1
			textStart = i
			continue
		}
		j := i + 1
		closing := false
		if j < len(b) && b[j] == '/' {
			closing = true
			j++
		}
		k := j
		for k < len(b) && isNameByte(b[k]) {
			k++
		}
		if k == j {
			i++
			continue
		}
		name := strings.ToLower(string(b[j:k]))
		if closing {
			end := bytes.IndexByte(b[k:], '>')
			if end < 0 {
				errf(i, []string{"</" + name}, "</%s is missing its closing >", name)
				return s
			}
			at := i
			i = k + end + 1
			textStart = i
			if len(stack) == 0 || stack[len(stack)-1] != name {
				words := []string{"</" + name + ">"}
				if len(stack) == 0 {
					errf(at, words, "unexpected </%s>; no element is open here, remove it", name)
				} else {
					errf(at, words, "unexpected </%s>; expected </%s> to close the open <%s> first", name, stack[len(stack)-1], stack[len(stack)-1])
				}
				if idx := slices.Index(stack, name); idx >= 0 {
					stack, opened = stack[:idx], opened[:idx]
				}
				continue
			}
			stack, opened = stack[:len(stack)-1], opened[:len(opened)-1]
			switch name {
			case "pre", "code", "textarea":
				codeDepth--
			case "svg":
				svgDepth--
			case "template":
				tplDepth--
			case "a", "button", "h1", "h2", "h3", "h4", "h5", "h6":
				if n := len(needName); n > 0 && needName[n-1].tag == name {
					q := needName[n-1]
					needName = needName[:n-1]
					if !q.ok && !(q.exempt && !q.child) {
						if name[0] == 'h' {
							errf(q.off, []string{"<" + name}, "empty <%s>; give the heading text or remove it", name)
						} else {
							errf(q.off, []string{"<" + name}, "<%s> has no accessible text; add text, a title attribute, an <img> alt or an svg <title>", name)
						}
					}
				}
			}
			continue
		}
		attrs, end, selfClose, ok := parseAttrs(b, k)
		if !ok {
			errf(i, []string{"<" + name}, "<%s is missing its closing > or a quote", name)
			return s
		}
		at := i
		i = end
		textStart = i
		var cls, id, style, href, alt, metaName, metaContent, rel, title, typ, hreflang string
		hasAlt, hidden, scripted := false, false, false
		for k := range needName {
			needName[k].child = true
		}
		for _, a := range attrs {
			switch a.name {
			case "class":
				cls = a.val
			case "id":
				id = a.val
			case "style":
				style = a.val
			case "href", "src", "action", "poster":
				if a.val != "" {
					s.Links = append(s.Links, a.val)
				} else {
					errf(at, []string{a.name + `=""`, "]()"}, "empty %s on <%s>; set a target or remove the attribute", a.name, name)
				}
				if a.name == "href" {
					href = a.val
					if msg := badContactLink(a.val); msg != "" {
						errf(at, linkWords(a.val), "%s", msg)
					}
				}
			case "srcset":
				for _, part := range strings.Split(a.val, ",") {
					if f := strings.Fields(part); len(f) > 0 {
						s.Links = append(s.Links, f[0])
					}
				}
			case "alt":
				alt, hasAlt = a.val, true
			case "name", "property":
				metaName = a.val
			case "content":
				metaContent = a.val
			case "rel":
				rel = a.val
			case "title":
				title = a.val
			case "type":
				typ = a.val
			case "hreflang":
				hreflang = a.val
			case "hidden":
				hidden = true
			case "for":
				if name == "label" {
					labels = append(labels, nameReq{tag: a.val, off: at})
				}
			}
			if strings.HasPrefix(a.name, "data-") {
				s.Data = append(s.Data, a.name)
				scripted = true
			}
		}
		if id != "" {
			if seenIDs[id] {
				errf(at, []string{"{#" + id + "}", `id="` + id + `"`}, "duplicate id %q on this page; ids must be unique", id)
			}
			seenIDs[id] = true
			s.IDs = append(s.IDs, id)
		}
		if cls != "" {
			s.Classes = append(s.Classes, strings.Fields(cls)...)
		}
		if style != "" {
			for _, m := range varDefRE.FindAllStringSubmatch(style, -1) {
				s.VarDefs = append(s.VarDefs, m[1])
			}
			for _, m := range varUseRE.FindAllStringSubmatch(style, -1) {
				s.VarUses = append(s.VarUses, m[1])
				if m[2] == ")" {
					s.VarBare = append(s.VarBare, m[1])
				}
			}
		}
		switch name {
		case "img":
			s.Imgs++
			if !hasAlt {
				errf(at, []string{"<img"}, "<img> without alt attribute; describe the image, or alt=\"\" if it is decorative")
			}
			if strings.TrimSpace(alt) != "" {
				for k := range needName {
					needName[k].ok = true
				}
			}
		case "iframe":
			if strings.TrimSpace(title) == "" && tplDepth == 0 {
				errf(at, []string{"<iframe"}, "<iframe> has no title; describe its content in a title attribute")
			}
		case "link":
			switch strings.ToLower(rel) {
			case "canonical":
				s.Canon = append(s.Canon, href)
			case "alternate":
				if hreflang != "" {
					s.Alts = append(s.Alts, hreflang+" "+href)
				}
			}
		case "a", "button", "h1", "h2", "h3", "h4", "h5", "h6":
			if name == "h1" && tplDepth == 0 {
				s.H1++
				if s.H1 == 2 {
					s.H1Off = at
				}
			}
			if !voidTags[name] && !selfClose {
				needName = append(needName, nameReq{tag: name, off: at, ok: strings.TrimSpace(title) != "" || tplDepth > 0, exempt: id != "" || hidden || scripted})
			}
		case "meta":
			switch strings.ToLower(metaName) {
			case "description":
				s.Desc = metaContent
			case "robots":
				s.NoIndex = strings.Contains(metaContent, "noindex")
			case "og:url":
				s.OGURL = metaContent
			}
			if strings.HasPrefix(metaContent, "https://") || strings.HasPrefix(metaContent, "http://") {
				s.Links = append(s.Links, metaContent)
			}
		}
		if voidTags[name] || selfClose {
			continue
		}
		switch name {
		case "script", "style", "title", "textarea":
			closeAt := indexFold(b[i:], []byte("</"+name))
			if closeAt < 0 {
				errf(at, []string{"<" + name}, "<%s> is never closed; add </%s>", name, name)
				return s
			}
			body := b[i : i+closeAt]
			switch name {
			case "title":
				if svgDepth > 0 {
					if len(bytes.TrimSpace(body)) > 0 {
						for k := range needName {
							needName[k].ok = true
						}
					}
					break
				}
				s.Titles++
				s.Title = html.UnescapeString(strings.TrimSpace(string(body)))
			case "script":
				switch typ {
				case "", "module":
					words(body, func(w []byte) { s.Words = append(s.Words, string(w)) })
				case "application/ld+json":
					if msg := checkJSONLD(body); msg != "" {
						errf(at, []string{"ld+json"}, "%s", msg)
					}
				}
			case "style":
				errf(at, []string{"<style"}, "inline <style> is not allowed; move the CSS to the site stylesheet in assets/")
			}
			i += closeAt
			textStart = i
			stack, opened = append(stack, name), append(opened, at)
			continue
		}
		switch name {
		case "pre", "code":
			codeDepth++
		case "svg":
			svgDepth++
		case "template":
			tplDepth++
		}
		stack, opened = append(stack, name), append(opened, at)
	}
	checkText(textStart, b[textStart:])
	for _, l := range labels {
		if !seenIDs[l.tag] {
			errf(l.off, []string{`for="` + l.tag + `"`}, "<label for=%q> points to no element with that id%s", l.tag, suggest(l.tag, s.IDs))
		}
	}
	if len(stack) > 0 {
		n := stack[len(stack)-1]
		errf(opened[len(opened)-1], []string{"<" + n}, "<%s> is never closed; add </%s>", n, n)
	}
	s.Classes = compactSorted(s.Classes)
	s.Data = compactSorted(s.Data)
	s.VarDefs = compactSorted(s.VarDefs)
	s.VarUses = compactSorted(s.VarUses)
	s.VarBare = compactSorted(s.VarBare)
	s.Words = compactSorted(s.Words)
	return s
}

type attr struct {
	name string
	val  string
}

func parseAttrs(b []byte, i int) ([]attr, int, bool, bool) {
	var out []attr
	for i < len(b) {
		for i < len(b) && isSpace(b[i]) {
			i++
		}
		if i >= len(b) {
			return nil, 0, false, false
		}
		if b[i] == '>' {
			return out, i + 1, false, true
		}
		if b[i] == '/' && i+1 < len(b) && b[i+1] == '>' {
			return out, i + 2, true, true
		}
		j := i
		for j < len(b) && !isSpace(b[j]) && b[j] != '=' && b[j] != '>' && !(b[j] == '/' && j+1 < len(b) && b[j+1] == '>') {
			j++
		}
		if j == i {
			i++
			continue
		}
		a := attr{name: strings.ToLower(string(b[i:j]))}
		i = j
		for i < len(b) && isSpace(b[i]) {
			i++
		}
		if i < len(b) && b[i] == '=' {
			i++
			for i < len(b) && isSpace(b[i]) {
				i++
			}
			if i < len(b) && (b[i] == '"' || b[i] == '\'') {
				q := b[i]
				end := bytes.IndexByte(b[i+1:], q)
				if end < 0 {
					return nil, 0, false, false
				}
				a.val = string(b[i+1 : i+1+end])
				i += end + 2
			} else {
				k := i
				for k < len(b) && !isSpace(b[k]) && b[k] != '>' {
					k++
				}
				a.val = string(b[i:k])
				i = k
			}
			if strings.IndexByte(a.val, '&') >= 0 {
				a.val = html.UnescapeString(a.val)
			}
		}
		out = append(out, a)
	}
	return nil, 0, false, false
}

type nameReq struct {
	tag    string
	off    int
	ok     bool
	exempt bool
	child  bool
}

var telRE = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
var emailRE = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}$`)

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func badContactLink(href string) string {
	switch {
	case hasPrefixFold(href, "tel:"):
		if !telRE.MatchString(href[4:]) {
			return fmt.Sprintf("phone link %s is not an international number; write tel:+<country code><number> with digits only", href)
		}
	case hasPrefixFold(href, "mailto:"):
		addrs, _, _ := strings.Cut(href[7:], "?")
		if dec, err := url.PathUnescape(addrs); err == nil {
			addrs = dec
		}
		for a := range strings.SplitSeq(addrs, ",") {
			if !emailRE.MatchString(strings.TrimSpace(a)) {
				return fmt.Sprintf("email link %s has an invalid address %q", href, strings.TrimSpace(a))
			}
		}
	}
	return ""
}

func checkJSONLD(body []byte) string {
	if json.Valid(body) && plainJSONLD(body) {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Sprintf("JSON-LD is not valid JSON: %v", err)
	}
	roots, ok := v.([]any)
	if !ok {
		roots = []any{v}
	}
	for _, r := range roots {
		m, ok := r.(map[string]any)
		if !ok {
			return "JSON-LD must be an object or a list of objects"
		}
		if c, _ := m["@context"].(string); c != "https://schema.org" && c != "https://schema.org/" {
			return `JSON-LD needs "@context": "https://schema.org"`
		}
	}
	if p := emptyJSON(v, ""); p != "" {
		return fmt.Sprintf("JSON-LD value %s is empty; fill it or leave the property out", p)
	}
	return ""
}

func emptyJSON(v any, at string) string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return orStr(at, "(root)")
		}
	case []any:
		if len(t) == 0 {
			return orStr(at, "(root)")
		}
		for i, e := range t {
			if p := emptyJSON(e, fmt.Sprintf("%s[%d]", at, i)); p != "" {
				return p
			}
		}
	case map[string]any:
		if len(t) == 0 {
			return orStr(at, "(root)")
		}
		for _, k := range sortedKeys(t) {
			if p := emptyJSON(t[k], at+"."+k); p != "" {
				return p
			}
		}
	case nil:
		return orStr(at, "(root)")
	}
	return ""
}

func plainJSONLD(b []byte) bool {
	var stack []byte
	contexts, roots := 0, 0
	key := false
	i := 0
	skip := func() {
		for i < len(b) && isSpace(b[i]) {
			i++
		}
	}
	for skip(); i < len(b); skip() {
		switch c := b[i]; c {
		case '{', '[':
			i++
			skip()
			if i < len(b) && (b[i] == '}' || b[i] == ']') {
				return false
			}
			if c == '{' && (len(stack) == 0 || len(stack) == 1 && stack[0] == '[') {
				roots++
			}
			stack = append(stack, c)
			key = c == '{'
		case '}', ']':
			stack = stack[:len(stack)-1]
			i++
		case ',':
			key = len(stack) > 0 && stack[len(stack)-1] == '{'
			i++
		case ':':
			i++
		case '"':
			end := i + 1
			for end < len(b) && b[end] != '"' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			s := b[i+1 : min(end, len(b))]
			if len(bytes.TrimSpace(s)) == 0 {
				return false
			}
			top := len(stack) == 1 && stack[0] == '{' || len(stack) == 2 && stack[0] == '[' && stack[1] == '{'
			if key && top && string(s) == "@context" {
				j := end + 1
				for j < len(b) && (isSpace(b[j]) || b[j] == ':') {
					j++
				}
				if !bytes.HasPrefix(b[j:], []byte(`"https://schema.org"`)) && !bytes.HasPrefix(b[j:], []byte(`"https://schema.org/"`)) {
					return false
				}
				contexts++
			}
			key = false
			i = end + 1
		case 'n':
			return false
		default:
			i++
		}
	}
	return roots > 0 && contexts == roots
}
