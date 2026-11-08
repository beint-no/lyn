package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkSVG(b []byte) (int, string) {
	var stack []string
	roots := 0
	anyEntity := false
	at := func(i int) int { return 1 + bytes.Count(b[:min(i, len(b))], []byte("\n")) }
	entity := func(s []byte, i int) bool {
		end := bytes.IndexByte(s[i:], ';')
		if end < 2 || end > 32 {
			return false
		}
		name := string(s[i+1 : i+end])
		switch {
		case name == "amp" || name == "lt" || name == "gt" || name == "quot" || name == "apos":
			return true
		case strings.HasPrefix(name, "#x"):
			return len(name) > 2 && strings.Trim(name[2:], "0123456789abcdefABCDEF") == ""
		case strings.HasPrefix(name, "#"):
			return len(name) > 1 && strings.Trim(name[1:], "0123456789") == ""
		}
		return anyEntity
	}
	text := func(start, end int) (int, string) {
		for i := start; i < end; {
			k := bytes.IndexByte(b[i:end], '&')
			if k < 0 {
				break
			}
			if !entity(b[:end], i+k) {
				return at(i + k), "unescaped & in text; write &amp;"
			}
			i += k + 1
		}
		return 0, ""
	}
	externalCSS := func(v []byte) bool {
		if !bytes.Contains(v, []byte("url(")) && !bytes.Contains(v, []byte("@import")) && !bytes.Contains(v, []byte("URL(")) {
			return false
		}
		l := bytes.ToLower(v)
		return bytes.Contains(l, []byte("url(http")) || bytes.Contains(l, []byte("url(//")) || bytes.Contains(l, []byte("url(\"http")) || bytes.Contains(l, []byte("url('http")) || bytes.Contains(l, []byte("@import"))
	}
	external := func(v string) bool {
		l := strings.ToLower(strings.TrimSpace(v))
		return strings.HasPrefix(l, "http:") || strings.HasPrefix(l, "https:") || strings.HasPrefix(l, "//")
	}
	i, textStart := 0, 0
	for i < len(b) {
		j := bytes.IndexByte(b[i:], '<')
		if j < 0 {
			break
		}
		i += j
		if len(stack) > 0 {
			if n, msg := text(textStart, i); msg != "" {
				return n, msg
			}
		}
		rest := b[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest, []byte("-->"))
			if end < 0 {
				return at(i), "comment is never closed"
			}
			i += end + 3
			textStart = i
			continue
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			end := bytes.Index(rest, []byte("]]>"))
			if end < 0 {
				return at(i), "CDATA section is never closed"
			}
			i += end + 3
			textStart = i
			continue
		case bytes.HasPrefix(rest, []byte("<?")):
			end := bytes.Index(rest, []byte("?>"))
			if end < 0 {
				return at(i), "processing instruction is never closed"
			}
			i += end + 2
			textStart = i
			continue
		case bytes.HasPrefix(rest, []byte("<!")):
			depth, k := 0, 0
			for k = 0; k < len(rest); k++ {
				if rest[k] == '[' {
					depth++
					anyEntity = true
				} else if rest[k] == ']' {
					depth--
				} else if rest[k] == '>' && depth == 0 {
					break
				}
			}
			i += k + 1
			textStart = i
			continue
		}
		closing := len(rest) > 1 && rest[1] == '/'
		k := i + 1
		if closing {
			k++
		}
		n := k
		for n < len(b) && isNameByte(b[n]) {
			n++
		}
		if n == k {
			return at(i), "unescaped < in text; write &lt;"
		}
		name := string(b[k:n])
		if closing {
			end := bytes.IndexByte(b[n:], '>')
			if end < 0 || len(bytes.TrimSpace(b[n:n+end])) > 0 {
				return at(i), fmt.Sprintf("malformed closing tag </%s", name)
			}
			if len(stack) == 0 || stack[len(stack)-1] != name {
				want := "nothing"
				if len(stack) > 0 {
					want = "</" + stack[len(stack)-1] + ">"
				}
				return at(i), fmt.Sprintf("unexpected </%s>; expected %s", name, want)
			}
			stack = stack[:len(stack)-1]
			i = n + end + 1
			textStart = i
			continue
		}
		local := name[strings.LastIndexByte(name, ':')+1:]
		if local == "script" {
			return at(i), "<script> is not allowed in SVG files"
		}
		p := n
		hasViewBox := false
		self := false
		for {
			s := p
			for p < len(b) && isSpace(b[p]) {
				p++
			}
			if p >= len(b) {
				return at(i), fmt.Sprintf("<%s> is never closed with >", name)
			}
			if b[p] == '>' {
				p++
				break
			}
			if b[p] == '/' && p+1 < len(b) && b[p+1] == '>' {
				p += 2
				self = true
				break
			}
			if p == s {
				return at(p), fmt.Sprintf("attributes of <%s> must be separated by spaces", name)
			}
			a := p
			for p < len(b) && isNameByte(b[p]) {
				p++
			}
			if a == p {
				return at(p), fmt.Sprintf("invalid character %q in <%s>", b[p], name)
			}
			attrName := string(b[a:p])
			for p < len(b) && isSpace(b[p]) {
				p++
			}
			if p >= len(b) || b[p] != '=' {
				return at(a), fmt.Sprintf("attribute %s in <%s> has no value", attrName, name)
			}
			p++
			for p < len(b) && isSpace(b[p]) {
				p++
			}
			if p >= len(b) || (b[p] != '"' && b[p] != '\'') {
				return at(a), fmt.Sprintf("attribute %s in <%s> must be quoted", attrName, name)
			}
			q := b[p]
			end := bytes.IndexByte(b[p+1:], q)
			if end < 0 {
				return at(a), fmt.Sprintf("attribute %s in <%s> is never closed", attrName, name)
			}
			val := b[p+1 : p+1+end]
			if bytes.IndexByte(val, '<') >= 0 {
				return at(a), fmt.Sprintf("unescaped < in attribute %s; write &lt;", attrName)
			}
			for x := 0; x < len(val); {
				k := bytes.IndexByte(val[x:], '&')
				if k < 0 {
					break
				}
				if !entity(val, x+k) {
					return at(a), fmt.Sprintf("unescaped & in attribute %s; write &amp;", attrName)
				}
				x += k + 1
			}
			switch {
			case hasPrefixFold(attrName, "on"):
				return at(a), fmt.Sprintf("event handler %s is not allowed in SVG files", attrName)
			case attrName == "viewBox":
				hasViewBox = len(bytes.Fields(val)) == 4 || len(bytes.Split(val, []byte(","))) == 4
			case (attrName == "href" || strings.HasSuffix(attrName, ":href")) && external(string(val)):
				return at(a), fmt.Sprintf("external reference %s; SVG files must be self-contained", val)
			case externalCSS(val):
				return at(a), "external reference in a style; SVG files must be self-contained"
			}
			p += end + 2
		}
		if len(stack) == 0 {
			roots++
			if roots > 1 {
				return at(i), "more than one root element"
			}
			if local != "svg" {
				return at(i), fmt.Sprintf("root element is <%s>, not <svg>", name)
			}
			if !hasViewBox {
				return at(i), `<svg> has no viewBox, so it cannot scale; add viewBox="0 0 <width> <height>"`
			}
		}
		i = p
		textStart = i
		if !self {
			stack = append(stack, name)
			if local == "style" {
				end := bytes.Index(b[i:], []byte("</"+name))
				if end < 0 {
					return at(i), "<style> is never closed"
				}
				if externalCSS(b[i : i+end]) {
					return at(i), "external reference in <style>; SVG files must be self-contained"
				}
				i += end
				textStart = i
			}
		}
	}
	if len(stack) > 0 {
		return at(len(b)), fmt.Sprintf("<%s> is never closed", stack[len(stack)-1])
	}
	if roots == 0 {
		return 1, "no <svg> element"
	}
	return 0, ""
}

func svgSource(file string) string {
	if strings.HasSuffix(file, ".svg") {
		return file
	}
	return ""
}

func (p *Project) svgChecked(file string, b []byte) {
	res := ""
	if line, msg := checkSVG(b); msg != "" {
		res = fmt.Sprintf("%s:%d: %s", file, line, msg)
	}
	p.svgMu.Lock()
	if p.svgRes == nil {
		p.svgRes = map[string]string{}
	}
	p.svgRes[file] = res
	p.svgMu.Unlock()
}

func (p *Project) checkSVGs(old, ns *State) {
	type job struct{ file, stamp string }
	var jobs []job
	for i, f := range p.static {
		if strings.HasSuffix(f, ".svg") {
			jobs = append(jobs, job{"static/" + f, p.staticStamps[i]})
		}
	}
	for _, pg := range p.pages {
		for _, r := range pg.Resources {
			if strings.HasSuffix(r.Name, ".svg") {
				jobs = append(jobs, job{r.src, r.stamp})
			}
		}
	}
	for _, name := range sortedKeys(p.assets) {
		if strings.HasSuffix(name, ".svg") {
			jobs = append(jobs, job{p.assets[name].src, p.assets[name].stamp})
		}
	}
	ns.SVGs = make(map[string]string, len(jobs))
	for _, j := range jobs {
		res, ok := p.svgRes[j.file]
		if !ok {
			res, ok = old.svgResult(j.file, j.stamp)
		}
		if !ok {
			if b, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(j.file))); err == nil {
				p.svgChecked(j.file, b)
				res = p.svgRes[j.file]
			}
		}
		if res != "" {
			p.errs.add("%s", res)
		}
		if j.stamp != "" {
			ns.SVGs[j.file] = j.stamp + "\x00" + res
		}
	}
}

func (s *State) svgResult(file, stamp string) (string, bool) {
	if s == nil || stamp == "" {
		return "", false
	}
	v, ok := s.SVGs[file]
	if !ok {
		return "", false
	}
	st, res, _ := strings.Cut(v, "\x00")
	return res, st == stamp
}
