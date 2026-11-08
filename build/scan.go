package build

import (
	"bytes"
	"fmt"
	"html"
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
	Errs    []string
	Imgs    int
}

var varUseRE = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)\s*([,)])`)
var varDefRE = regexp.MustCompile(`(?:^|;)\s*(--[A-Za-z0-9_-]+)\s*:`)
var wordRE = regexp.MustCompile(`[A-Za-z_$][\w$-]*`)

func scanHTML(b []byte) Scan {
	var s Scan
	var stack []string
	codeDepth := 0
	seenIDs := map[string]bool{}
	errf := func(format string, args ...any) {
		if len(s.Errs) < 20 {
			s.Errs = append(s.Errs, fmt.Sprintf(format, args...))
		}
	}
	textStart := 0
	checkText := func(t []byte) {
		if codeDepth > 0 || len(t) == 0 {
			return
		}
		for _, pat := range []string{"{{", "}}", "](", "**", "LYNSC"} {
			if i := bytes.Index(t, []byte(pat)); i >= 0 {
				snippet := strings.TrimSpace(string(t[max(0, i-30):min(len(t), i+30)]))
				errf("visible markup %q in page text: %q", pat, snippet)
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
		checkText(b[textStart:i])
		switch {
		case bytes.HasPrefix(b[i:], []byte("<!--")):
			end := bytes.Index(b[i:], []byte("-->"))
			if end < 0 {
				errf("unterminated HTML comment")
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
				errf("unterminated </%s>", name)
				return s
			}
			i = k + end + 1
			textStart = i
			if len(stack) == 0 || stack[len(stack)-1] != name {
				top := "nothing"
				if len(stack) > 0 {
					top = "</" + stack[len(stack)-1] + ">"
				}
				errf("unexpected </%s>; expected %s", name, top)
				if idx := slices.Index(stack, name); idx >= 0 {
					stack = stack[:idx]
				}
				continue
			}
			stack = stack[:len(stack)-1]
			if name == "pre" || name == "code" || name == "textarea" {
				codeDepth--
			}
			continue
		}
		attrs, end, selfClose, ok := parseAttrs(b, k)
		if !ok {
			errf("unterminated <%s> tag", name)
			return s
		}
		i = end
		textStart = i
		var cls, id, style, href, alt, metaName, metaContent, rel string
		hasAlt := false
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
					errf("empty %s on <%s>", a.name, name)
				}
				if a.name == "href" {
					href = a.val
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
			}
			if strings.HasPrefix(a.name, "data-") {
				s.Data = append(s.Data, a.name)
			}
		}
		_, _, _ = href, alt, rel
		if id != "" {
			if seenIDs[id] {
				errf("duplicate id %q", id)
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
				errf("<img> without alt attribute")
			}
		case "meta":
			switch strings.ToLower(metaName) {
			case "description":
				s.Desc = metaContent
			case "robots":
				s.NoIndex = strings.Contains(metaContent, "noindex")
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
				errf("unclosed <%s>", name)
				return s
			}
			body := b[i : i+closeAt]
			switch name {
			case "title":
				s.Titles++
				s.Title = html.UnescapeString(strings.TrimSpace(string(body)))
			case "script":
				typ := ""
				for _, a := range attrs {
					if a.name == "type" {
						typ = a.val
					}
				}
				if typ == "" || typ == "module" {
					for _, w := range wordRE.FindAll(body, -1) {
						s.Words = append(s.Words, string(w))
					}
				}
			case "style":
				errf("inline <style> is not allowed; put CSS in assets/")
			}
			i += closeAt
			textStart = i
			stack = append(stack, name)
			continue
		}
		if name == "pre" || name == "code" {
			codeDepth++
		}
		stack = append(stack, name)
	}
	checkText(b[textStart:])
	if len(stack) > 0 {
		errf("unclosed <%s>", stack[len(stack)-1])
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
