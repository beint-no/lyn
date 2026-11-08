package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Scan struct {
	Links     []string
	IDs       []string
	Classes   []string
	Data      []string
	VarDefs   []string
	VarUses   []string
	VarBare   []string
	Words     []string
	Title     string
	Titles    int
	Desc      string
	NoIndex   bool
	Errs      []ScanErr
	Imgs      int
	H1        int
	H1Off     int
	Canon     []string
	OGURL     string
	Alts      []string
	ImgSizes  []string
	ScriptIDs []string
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
	var stack []openTag
	var needName []nameReq
	var refs []nameReq
	codeDepth, svgDepth, tplDepth, headDepth, mainDepth := 0, 0, 0, 0, 0
	foreign, pOpen, linkOpen := 0, 0, 0
	doctype, charset, viewport := false, false, false
	var metas []string

	lastHeading := 0
	s.H1Off = -1
	seenIDs := map[string]bool{}
	popovers := map[string]bool{}
	errf := func(off int, words []string, format string, args ...any) {
		if len(s.Errs) < 20 {
			s.Errs = append(s.Errs, ScanErr{off, fmt.Sprintf(format, args...), words})
		}
	}
	if off := mojibake(b); off >= 0 {
		snippet := strings.ToValidUTF8(strings.TrimSpace(string(b[max(0, off-20):min(len(b), off+20)])), "")
		errf(off, []string{snippet, html.UnescapeString(snippet)}, "garbled text %q: UTF-8 was decoded as Latin-1 or a character was lost; retype the original characters", snippet)
	}
	textStart := 0
	checkText := func(start int, t []byte) {
		if len(t) == 0 {
			return
		}
		blank := len(bytes.TrimSpace(t)) == 0
		if len(needName) > 0 && !blank {
			for k := range needName {
				needName[k].ok = true
			}
		}
		if blank {
			return
		}
		if n := len(stack); n > 0 && stack[n-1].flags&eTextless != 0 && foreign == 0 {
			snippet := strings.TrimSpace(string(t[:min(len(t), 40)]))
			errf(start, []string{snippet}, "text %q directly inside <%s>; browsers move or drop it, so put it in %s", snippet, stack[n-1].name, childHint(stack[n-1].name))
		}
		if codeDepth > 0 {
			return
		}
		i, pat, kind := textProblem(t)
		if i < 0 {
			return
		}
		snippet := strings.ToValidUTF8(strings.TrimSpace(string(t[max(0, i-30):min(len(t), i+30)])), "")
		words := []string{snippet, html.UnescapeString(snippet)}
		switch kind {
		case "placeholder text":
			errf(start+i, words, "placeholder text in page: %q; write the real content", snippet)
		case "entity":
			errf(start+i, append(words, html.UnescapeString(pat)), "HTML entity %s is shown literally: %q; write the character itself or the entity once", html.UnescapeString(pat), snippet)
		default:
			errf(start+i, append(words, pat), "visible markup %q in page text: %q; the syntax was not rendered, fix or escape it", pat, snippet)
		}
	}
	pop := func(n int) {
		for _, o := range stack[n:] {
			if o.flags&eForeign != 0 {
				foreign--
			}
			switch o.name {
			case "p":
				pOpen--
			case "a", "button":
				linkOpen--

			case "pre", "code":
				codeDepth--
			case "svg":
				svgDepth--
			case "template":
				tplDepth--
			case "head":
				headDepth--
			case "main":
				mainDepth--
			}
		}
		stack = stack[:n]
	}
	i := 0
	for i < len(b) {
		next := bytes.IndexByte(b[i:], '<')
		if next < 0 {
			break
		}
		i += next
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
			if len(bytes.TrimSpace(b[:i])) == 0 && bytes.EqualFold(bytes.Join(bytes.Fields(b[i:i+end+1]), []byte(" ")), []byte("<!doctype html>")) {
				doctype = true
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
		eid := lookupName(&elemTable, b[j:k])
		var name string
		var flags uint16
		if eid >= 0 {
			name, flags = elemTable.names[eid], elems[eid].flags
		} else {
			name = strings.ToLower(string(b[j:k]))
		}
		if closing {
			end := bytes.IndexByte(b[k:], '>')
			if end < 0 {
				errf(i, []string{"</" + name}, "</%s is missing its closing >", name)
				return s
			}
			at := i
			i = k + end + 1
			textStart = i
			if len(stack) == 0 || stack[len(stack)-1].name != name {
				words := []string{"</" + name + ">"}
				if len(stack) == 0 {
					errf(at, words, "unexpected </%s>; no element is open here, remove it", name)
				} else {
					top := stack[len(stack)-1].name
					errf(at, words, "unexpected </%s>; expected </%s> to close the open <%s> first", name, top, top)
				}
				for idx := len(stack) - 1; idx >= 0; idx-- {
					if stack[idx].name == name {
						pop(idx)
						break
					}
				}
				continue
			}
			pop(len(stack) - 1)
			switch name {
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
		var cls, id, style, href, alt, metaName, metaContent, rel, title, typ, hreflang, src, srcset, sizes string
		hasAlt, hidden, scripted, hasW, hasH, hasItemprop, hasPopover, hasLang := false, false, false, false, false, false, false, false
		w, h := -1, -1
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
					if a.val == "#" {
						errf(at, []string{`href="#"`, "](#)"}, "link to # goes nowhere; link to a real target, or use a <button> for an action")
					}
					if msg := badContactLink(a.val); msg != "" {
						errf(at, linkWords(a.val), "%s", msg)
					}
				}
				if a.name == "src" {
					src = a.val
				}

			case "srcset":
				srcset = a.val
				for _, part := range strings.Split(a.val, ",") {
					if f := strings.Fields(part); len(f) > 0 {
						s.Links = append(s.Links, f[0])
					}
				}
			case "sizes":
				sizes = a.val
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
			case "width":
				hasW = true
				w = atoiOr(a.val, -1)
			case "height":
				hasH = true
				h = atoiOr(a.val, -1)
			case "itemprop":
				hasItemprop = true
			case "lang":
				hasLang = a.val != ""
			case "popover":
				hasPopover = true
			case "for":
				if name == "label" {
					refs = append(refs, nameReq{tag: a.val, off: at, attr: "for"})
				}
			}
			if strings.HasPrefix(a.name, "data-") {
				s.Data = append(s.Data, a.name)
				scripted = true
			}
			if a.id >= 0 && attrInfos[a.id].idref {
				for ref := range strings.FieldsSeq(a.val) {
					refs = append(refs, nameReq{tag: ref, off: at, attr: a.name})
				}
			}
		}
		if foreign == 0 {
			for _, a := range attrs {
				switch a.name {
				case "alt", "title", "aria-label", "placeholder", "content":
					if a.name == "content" && !textMeta(metaName) {
						continue
					}
					if k, pat, kind := textProblem(a.val); k >= 0 && kind != "entity" && !(a.name == "placeholder" && kind == "placeholder text") {
						if kind == "" {
							kind = fmt.Sprintf("visible markup %q", pat)
						}
						errf(attrOffset(b[at:end], a.name)+at, []string{a.val}, "%s in %s=%q; write the real text", kind, a.name, a.val)
					}
				}
			}
		}
		if hasPopover && id != "" {
			popovers[id] = true
		}
		if foreign == 0 {
			if flags&eForeign == 0 {
				lintAttrs(&tagLint{errf: errf, at: at, tag: b[at:end], id: eid, name: name, attrs: attrs})
			}
			if msg := contentModel(eid, name, stack, name == "input" && strings.EqualFold(typ, "hidden"), pOpen, linkOpen); msg != "" {
				errf(at, []string{"<" + name}, "%s", msg)
			}
			if headDepth == 0 && len(stack) > 0 {
				switch name {
				case "title", "base", "style":
					errf(at, []string{"<" + name}, "<%s> belongs in <head>; move it there", name)
				case "meta":
					if !hasItemprop {
						errf(at, []string{"<meta"}, "<meta> outside <head> is ignored; move it into <head>")
					}
				case "link":
					if !hasItemprop && !bodyOKRel(rel) {
						errf(at, []string{"<link"}, "<link rel=%q> outside <head>; move it into <head>", rel)
					}
				}
			}
			if len(stack) > 0 {
				stack[len(stack)-1].kids++
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
		if srcset != "" || sizes != "" {
			if msg := srcsetProblem(srcset, sizes); msg != "" {
				errf(at, []string{"srcset=", "<" + name}, "%s", msg)
			}
		}
		switch name {
		case "html":
			if !hasLang {
				errf(at, []string{"<html"}, "<html> has no lang attribute; set it to the page language so browsers and screen readers use the right rules")
			}
		case "img":
			s.Imgs++
			if !hasAlt {
				errf(at, []string{"<img"}, "<img> without alt attribute; describe the image, or alt=\"\" if it is decorative")
			}
			if !hasW || !hasH {
				errf(at, []string{"<img", src}, "<img> without width and height; set both to the image's size so the page does not jump while it loads")
			} else if w > 0 && h > 0 && src != "" && !isExternal(src) && !strings.HasPrefix(src, "data:") {
				s.ImgSizes = append(s.ImgSizes, src+" "+strconv.Itoa(w)+" "+strconv.Itoa(h))
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
			if name[0] == 'h' && mainDepth > 0 && svgDepth == 0 && tplDepth == 0 {
				level := int(name[1] - '0')
				if level > lastHeading+1 && lastHeading > 0 {
					errf(at, []string{"<" + name, strings.Repeat("#", level) + " "}, "<%s> follows <h%d> and skips a heading level; use <h%d> or restructure the headings", name, lastHeading, lastHeading+1)
				}
				lastHeading = level
			}
			if name == "h1" && tplDepth == 0 {
				s.H1++
				if s.H1 == 2 {
					s.H1Off = at
				}
			}
			if flags&eVoid == 0 && !selfClose {
				needName = append(needName, nameReq{tag: name, off: at, ok: strings.TrimSpace(title) != "" || tplDepth > 0, exempt: id != "" || hidden || scripted})
			}
		case "meta":
			key := strings.ToLower(metaName)
			for _, a := range attrs {
				switch a.name {
				case "charset":
					charset = true
				case "media":
					key += " " + a.val
				}
			}
			if key == "viewport" {
				viewport = true
			}
			if metaName != "" && !repeatableMeta(key) {
				if slices.Contains(metas, key) {
					errf(at, []string{metaName}, "<meta> %s appears twice; keep one", metaName)
				}
				metas = append(metas, key)
			}
			if absoluteMeta(key) && !hasPrefixFold(metaContent, "https://") && !hasPrefixFold(metaContent, "http://") {
				errf(at, []string{metaName}, "<meta> %s must be an absolute https:// URL, not %q; social sites ignore relative URLs", metaName, metaContent)
			}
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
		if flags&eVoid != 0 || selfClose {
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
				case "", "module", "text/javascript":
					words(body, func(w []byte) { s.Words = append(s.Words, string(w)) })
					if sr := checkScript(body); sr.msg != "" {
						errf(i+sr.off, []string{"<script"}, "%s", sr.msg)
					} else {
						s.ScriptIDs = append(s.ScriptIDs, sr.ids...)
					}
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
			stack = append(stack, openTag{name: name, off: at, id: eid, flags: flags})
			continue
		}
		if flags&eForeign != 0 {
			foreign++
		}
		switch name {
		case "p":
			pOpen++
		case "a", "button":
			linkOpen++

		case "pre", "code":
			codeDepth++
		case "svg":
			svgDepth++
		case "template":
			tplDepth++
		case "head":
			headDepth++
		case "main":
			mainDepth++
			lastHeading = 0
		}
		stack = append(stack, openTag{name: name, off: at, id: eid, flags: flags})
	}
	checkText(textStart, b[textStart:])
	switch {
	case !doctype:
		errf(0, []string{"<!doctype"}, "the page does not start with <!doctype html>; add it so browsers do not use quirks mode")
	case !charset:
		errf(0, []string{"<head"}, `the page has no <meta charset="utf-8">; add it at the top of <head>`)
	case !viewport:
		errf(0, []string{"<head"}, `the page has no <meta name="viewport" content="width=device-width, initial-scale=1">; add it to <head> so phones do not show a zoomed-out desktop page`)
	}
	for _, l := range refs {
		if !seenIDs[l.tag] {
			if l.attr == "for" {
				errf(l.off, []string{`for="` + l.tag + `"`}, "<label for=%q> points to no element with that id%s", l.tag, suggest(l.tag, s.IDs))
			} else {
				errf(l.off, []string{l.attr + `="` + l.tag + `"`}, "%s=%q points to no element with that id on this page%s", l.attr, l.tag, suggest(l.tag, s.IDs))
			}
		} else if (l.attr == "popovertarget") && !popovers[l.tag] {
			errf(l.off, []string{l.attr + `="` + l.tag + `"`}, "popovertarget=%q points to an element without a popover attribute; add popover to #%s", l.tag, l.tag)
		}
	}
	if len(stack) > 0 {
		n := stack[len(stack)-1]
		errf(n.off, []string{"<" + n.name}, "<%s> is never closed; add </%s>", n.name, n.name)
	}
	s.Classes = compactSorted(s.Classes)
	s.Data = compactSorted(s.Data)
	s.VarDefs = compactSorted(s.VarDefs)
	s.VarUses = compactSorted(s.VarUses)
	s.VarBare = compactSorted(s.VarBare)
	s.Words = compactSorted(s.Words)
	s.ImgSizes = compactSorted(s.ImgSizes)
	s.ScriptIDs = compactSorted(s.ScriptIDs)
	return s
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func bodyOKRel(rel string) bool {
	if rel == "" {
		return false
	}
	for tok := range strings.FieldsSeq(strings.ToLower(rel)) {
		switch tok {
		case "dns-prefetch", "modulepreload", "pingback", "preconnect", "prefetch", "preload", "stylesheet":
		default:
			return false
		}
	}
	return true
}

type attr struct {
	name string
	val  string
	id   int16
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
		a := attr{id: int16(lookupName(&attrTable, b[i:j]))}
		if a.id >= 0 {
			a.name = attrTable.names[a.id]
		} else {
			a.name = strings.ToLower(string(b[i:j]))
		}
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
	attr   string
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
		return mailtoProblem(href)
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

func repeatableMeta(key string) bool {
	for _, p := range []string{"og:image", "og:video", "og:audio", "og:locale:alternate", "article:tag", "article:author", "book:", "music:", "video:", "theme-color "} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func absoluteMeta(key string) bool {
	switch key {
	case "og:url", "og:image", "og:image:url", "og:image:secure_url", "og:video", "og:audio", "twitter:image":
		return true
	}
	return false
}

func textMeta(name string) bool {
	switch strings.ToLower(name) {
	case "description", "og:title", "og:description", "twitter:title", "twitter:description", "og:image:alt", "twitter:image:alt":
		return true
	}
	return false
}

func attrOffset(tag []byte, name string) int {
	for i := 0; i+len(name) < len(tag); i++ {
		if i > 0 && isSpace(tag[i-1]) && tag[i+len(name)] == '=' && string(tag[i:i+len(name)]) == name {
			j := i + len(name) + 1
			if j < len(tag) && (tag[j] == '"' || tag[j] == '\'') {
				j++
			}
			return j
		}
	}
	return 0
}
