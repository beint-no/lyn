package build

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/beint-no/lyn/yamlite"
)

var markdownClasses = map[string]bool{"footnotes": true, "footnote-ref": true, "footnote-backref": true}

func (p *Project) check(old, ns *State, files map[string]bool) {
	e := p.errs
	records := ns.Outputs
	outKeys := sortedKeys(records)

	redirects := p.loadRedirects(files)
	p.checkHeaders(files)

	jsText := &bytes.Buffer{}
	scripts := map[string][]byte{}
	for _, a := range p.assets {
		if strings.HasSuffix(a.Name, ".js") {
			jsText.Write(a.data)
			jsText.WriteByte('\n')
			if src, err := os.ReadFile(filepath.Join(p.Root, a.src)); err == nil {
				scripts["assets/"+a.Name] = src
			}
		}
	}
	for _, f := range p.static {
		if strings.HasSuffix(f, ".js") {
			b, _ := os.ReadFile(filepath.Join(p.Root, "static", f))
			jsText.Write(b)
			jsText.WriteByte('\n')
			scripts["static/"+f] = b
		}
	}
	p.checkStaticScripts(scripts)
	p.checkDates()
	redirects.chains(p.errs.add)
	for _, f := range p.workerScripts() {
		b, _ := os.ReadFile(f)
		jsText.Write(b)
		jsText.WriteByte('\n')
	}
	jsWords := map[string]bool{}
	words(jsText.Bytes(), func(w []byte) {
		if !jsWords[string(w)] {
			jsWords[string(w)] = true
		}
	})
	for _, r := range records {
		for _, w := range r.Scan.Words {
			jsWords[w] = true
		}
	}

	linked := map[string]bool{}
	titles := map[string]string{}
	descs := map[string]string{}
	usedBodies := usedKeys(records, func(r *OutRecord) []string { return r.Bodies })
	usedI18n := usedKeys(records, func(r *OutRecord) []string { return r.I18n })
	reads := usedKeys(records, func(r *OutRecord) []string { return r.Reads })
	usedRes := usedKeys(records, func(r *OutRecord) []string { return r.Res })
	classes := map[string]string{}
	dataAttrs := map[string]string{}
	varDefs := map[string]string{}
	varUses := map[string]bool{}
	varBare := map[string]string{}
	usedLayouts := map[string]bool{}

	pageByKey := map[string]*Page{}
	for _, pg := range p.pages {
		pageByKey[pageKey(pg)] = pg
	}
	for _, s := range p.Sites {
		pageByKey[pageKey(s.notFound)] = s.notFound
	}

	var lastClasses, lastData []string
	for _, out := range outKeys {
		r := records[out]
		pg := pageByKey[r.Page]
		url := "/" + strings.TrimSuffix(out, "index.html")
		where := url
		if pg != nil && pg.File != "" {
			where = pg.File
		}
		for _, se := range r.Scan.Errs {
			e.issue(pageIssue{out: out, off: se.Off, words: se.Words, msg: se.Msg})
		}
		head, body := where, where
		if pg != nil {
			if l, err := p.layoutFor(pg); err == nil {
				usedLayouts[l.name] = true
				head, body = l.file, l.file
				if _, ok := p.tpl.base["layouts/base.html"]; ok && slices.Contains(l.files, p.tpl.fileIDs["layouts/base.html"]) {
					head = "layouts/base.html"
				}
			}
		}
		if r.Scan.Titles != 1 {
			e.at(head, fmt.Sprintf("page must have exactly one <title> (found %d)", r.Scan.Titles), pg)
		} else if r.Scan.Title == "" {
			e.issue(pageIssue{out: out, off: -1, kind: findTag, match: "title", msg: "<title> is empty; give the page a title or seoTitle"})
		} else if pg != nil && pg.Kind != "404" {
			tkey := pg.Lang.Code + "\x00" + r.Scan.Title
			if other, ok := titles[tkey]; ok {
				e.at(p.fmLoc(pg, "seoTitle", "title"), fmt.Sprintf("duplicate <title> %q (also on %s); make seoTitle or title unique", r.Scan.Title, other), nil)
			} else {
				titles[tkey] = where
			}
		}
		if pg != nil && pg.Kind != "404" {
			if !pg.NoIndex && strings.TrimSpace(r.Scan.Desc) == "" {
				e.at(head, `indexable page has no <meta name="description">; render .Description in the <head>`, pg)
			}
			if pg.NoIndex && !r.Scan.NoIndex {
				e.at(head, `noindex page must render <meta name="robots" content="noindex">; render it when .NoIndex is true`, pg)
			}
			if !pg.NoIndex {
				switch {
				case r.Scan.H1 == 0:
					e.at(body, "indexable page has no <h1>; render the page title as its main heading", pg)
				case r.Scan.H1 > 1:
					e.issue(pageIssue{out: out, off: r.Scan.H1Off, words: []string{"<h1", "# "}, msg: fmt.Sprintf("indexable page has %d <h1> elements; keep one and use <h2> for the others", r.Scan.H1)})
				}
				if d := strings.TrimSpace(r.Scan.Desc); d != "" {
					dkey := pg.Lang.Code + "\x00" + d
					if other, ok := descs[dkey]; ok {
						e.at(p.fmLoc(pg, "description"), fmt.Sprintf("duplicate meta description (also on %s); write a description for this page", other), nil)
					} else {
						descs[dkey] = where
					}
				}
			}
			p.checkSelfLinks(pg, r, out, head)
		}
		if pg != nil && pg.Kind == "404" && (len(r.Scan.Canon) > 0 || r.Scan.OGURL != "") {
			e.at(head, "the 404 page must not have a canonical link or og:url; it has no URL of its own", pg)
		}
		if !slices.Equal(r.Scan.Classes, lastClasses) {
			lastClasses = r.Scan.Classes
			for _, c := range r.Scan.Classes {
				if _, ok := classes[c]; !ok {
					classes[c] = out
				}
			}
		}
		if !slices.Equal(r.Scan.Data, lastData) {
			lastData = r.Scan.Data
			for _, d := range r.Scan.Data {
				if _, ok := dataAttrs[d]; !ok {
					dataAttrs[d] = out
				}
			}
		}
		for _, v := range r.Scan.VarDefs {
			varDefs[v] = where
		}
		for _, v := range r.Scan.VarUses {
			varUses[v] = true
		}
		for _, v := range r.Scan.VarBare {
			varBare[v] = where
		}
	}
	type linkJob struct {
		r     *OutRecord
		url   string
		where at
	}
	siblingURLs := p.siblingURLs()
	jobs := make(chan linkJob, 64)
	var wg sync.WaitGroup
	linkedParts := make([]map[string]bool, runtime.GOMAXPROCS(0))
	for w := range linkedParts {
		local := map[string]bool{}
		linkedParts[w] = local
		wg.Go(func() {
			for j := range jobs {
				var ids map[string]bool
				for _, l := range j.r.Scan.Links {
					if msg := externalLinkProblem(l); msg != "" {
						p.report(j.where, findLink, l, "%s", msg)
						continue
					}
					if p.checkSibling(l, j.where, siblingURLs) {
						continue
					}
					target, frag, ok := p.internalTarget(j.url, l, j.where)
					if !ok {
						continue
					}
					if target == "" {
						if ids == nil {
							ids = make(map[string]bool, len(j.r.Scan.IDs))
							for _, id := range j.r.Scan.IDs {
								ids[id] = true
							}
						}
						if frag != "" && !ids[frag] {
							p.report(j.where, findLink, l, "link to #%s, but this page has no element with that id%s", frag, suggest(frag, j.r.Scan.IDs))
						}
						continue
					}
					p.checkTarget(j.where, l, target, frag, files, records, redirects, local)
				}
			}
		})
	}
	for _, out := range outKeys {
		jobs <- linkJob{records[out], "/" + strings.TrimSuffix(out, "index.html"), at{out: out}}
	}
	close(jobs)
	wg.Wait()
	for _, part := range linkedParts {
		for k := range part {
			linked[k] = true
		}
	}
	for to, line := range redirects.targets {
		w := at{file: fmt.Sprintf("static/_redirects:%d", line)}
		target, frag, ok := p.internalTarget("/", to, w)
		if ok && target != "" {
			p.checkTarget(w, to, target, frag, files, records, nil, linked)
		}
	}

	for _, pg := range p.pages {
		if len(bytes.TrimSpace(pg.body)) > 0 && !usedBodies[pageKey(pg)] {
			e.add("%s: the page body is never rendered by its layout; remove it or render .Content", pg.File)
		}
	}
	for _, k := range p.i18nKeys {
		if !usedI18n[k] {
			e.add("%s: translation %q is never used; delete it from every i18n file", p.i18nLines[k], k)
		}
	}
	for _, root := range p.mapRoots {
		p.reportUnread(root, reads, jsWords)
	}
	setParams := map[string]bool{}
	for _, pg := range p.pages {
		for _, k := range pg.Params.Keys {
			setParams[k] = true
		}
		for _, k := range pg.toolParams.Keys {
			setParams[k] = true
		}
	}
	for _, k := range sortedKeys(p.pageParams) {
		if !setParams[k] {
			e.add("site.yml: pageParams.%s is declared but no page sets it; remove the declaration", k)
		}
	}
	for _, k := range sortedKeys(p.toolParams) {
		if !setParams[k] {
			e.add("site.yml: toolParams.%s is declared but no page sets it; remove the declaration", k)
		}
	}
	for name, l := range p.tpl.layouts {
		if !usedLayouts[name] {
			e.add("%s: layout is never used by any page; delete it", l.file)
		}
	}
	usedShortcodes := map[string]bool{}
	for _, ps := range ns.Pages {
		for _, sc := range ps.Shortcodes {
			usedShortcodes[sc] = true
		}
	}
	reach := map[*tmpl]bool{}
	var mark func(t *tmpl)
	mark = func(t *tmpl) {
		if reach[t] {
			return
		}
		reach[t] = true
		for _, c := range t.calls {
			mark(c)
		}
	}
	for name, l := range p.tpl.layouts {
		if usedLayouts[name] {
			mark(l)
		}
	}
	for name, sc := range p.tpl.shortcodes {
		if usedShortcodes[name] {
			mark(sc)
		} else {
			e.add("%s: shortcode is never used; delete it", sc.file)
		}
	}
	for _, pt := range p.tpl.partials {
		if !reach[pt] {
			e.add("%s: partial is never used; delete it", pt.file)
		}
	}
	for _, res := range p.resources {
		if !usedRes[resKey(res)] && !linked[res.RelPermalink] {
			if res.asset {
				e.add("assets/%s: asset is never used; delete it", res.Name)
			} else {
				e.add("%s: bundle file is never used; reference it from the page or delete it", res.src)
			}
		}
	}

	css := newCSSInfo()
	for _, a := range p.assets {
		if strings.HasSuffix(a.Name, ".css") && a.data != nil {
			src, err := os.ReadFile(filepath.Join(p.Root, a.src))
			if err != nil {
				src = a.data
			}
			if err := css.analyze("assets/"+a.Name, src); err != nil {
				e.add("assets/%s: %v", a.Name, err)
			}
		}
	}
	for _, f := range p.static {
		if strings.HasSuffix(f, ".css") {
			b, _ := os.ReadFile(filepath.Join(p.Root, "static", f))
			if err := css.analyze("static/"+f, b); err != nil {
				e.add("static/%s: %v", f, err)
			}
		}
	}
	for u, file := range css.urls {
		target := u
		if !strings.HasPrefix(u, "/") && !isExternal(u) {
			f, _, _ := locParts(file)
			target = path.Join("/", path.Dir(strings.TrimPrefix(strings.TrimPrefix(f, "assets/"), "static/")), u)
		}
		if isExternal(target) {
			continue
		}
		t, _, _ := strings.Cut(target, "?")
		t, _, _ = strings.Cut(t, "#")
		if !files[strings.TrimPrefix(t, "/")] {
			e.add("%s: url(%s) does not exist%s", file, u, p.nearestPath(t, files))
		}
		linked[t] = true
	}
	for _, pr := range css.probs {
		e.add("%s", pr)
	}
	p.checkIDRefs(records, outKeys, css, jsWords, scripts)
	p.checkImageSizes(records, outKeys, old, ns)
	for c, file := range css.classes {
		if _, ok := classes[c]; !ok && !jsWords[c] {
			e.add("%s: CSS class .%s is never used; delete the rule%s", file, c, suggest(c, keysOf(classes)))
		}
	}
	for c, out := range classes {
		if _, ok := css.classes[c]; !ok && !jsWords[c] && !markdownClasses[c] && !strings.HasPrefix(c, "language-") {
			p.report(at{out: out}, findClass, c, "class %q has no CSS rule; add one to the stylesheet or remove the class%s", c, suggest(c, keysOf(css.classes)))
		}
	}
	js := jsText.String()
	for v, file := range css.varDefs {
		varDefs[v] = file
	}
	for v := range css.varUses {
		varUses[v] = true
	}
	for v, file := range css.varBare {
		varBare[v] = file
	}
	for v, file := range varDefs {
		if !varUses[v] && !strings.Contains(js, v) {
			e.add("%s: custom property %s is never read; delete it", file, v)
		}
	}
	for v, file := range varBare {
		if _, ok := varDefs[v]; !ok && !strings.Contains(js, v) {
			e.add("%s: var(%s) has no fallback and %s is never defined; define it or add a fallback%s", file, v, v, suggest(v, keysOf(varDefs)))
		}
	}
	for d, out := range dataAttrs {
		camel := dataCamel(d)
		if !css.attrs[d] && !strings.Contains(js, d) && !jsWords[camel] && !jsWords[d] {
			p.report(at{out: out}, findAttr, d, "attribute %s is never used by CSS or scripts; remove it", d)
		}
	}
	for _, f := range p.static {
		if p.mustBeLinked(f) && !linked["/"+f] {
			e.add("static/%s: file is never referenced; link it or delete it", f)
		}
	}
}

func usedKeys(records map[string]*OutRecord, list func(*OutRecord) []string) map[string]bool {
	all := slices.Collect(maps.Values(records))
	parts := make([]map[string]bool, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for w := range parts {
		wg.Go(func() {
			set := map[string]bool{}
			var last []string
			for i := w; i < len(all); i += len(parts) {
				l := list(all[i])
				if slices.Equal(l, last) {
					continue
				}
				last = l
				for _, k := range l {
					set[k] = true
				}
			}
			parts[w] = set
		})
	}
	wg.Wait()
	out := parts[0]
	for _, set := range parts[1:] {
		maps.Copy(out, set)
	}
	return out
}

func words(b []byte, fn func([]byte)) {
	for i := 0; i < len(b); {
		c := b[i]
		if c != '_' && (c|0x20 < 'a' || c|0x20 > 'z') {
			i++
			continue
		}
		j := i + 1
		for j < len(b) && (b[j] == '_' || b[j] == '-' || b[j] >= '0' && b[j] <= '9' || b[j]|0x20 >= 'a' && b[j]|0x20 <= 'z') {
			j++
		}
		fn(b[i:j])
		i = j
	}
}

func dataCamel(attr string) string {
	parts := strings.Split(strings.TrimPrefix(attr, "data-"), "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func (p *Project) mustBeLinked(f string) bool {
	switch path.Ext(f) {
	case ".css", ".js", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".otf", ".mp4", ".webm", ".pdf":
	default:
		return false
	}
	return f != "apple-touch-icon.png" && !strings.HasPrefix(f, ".well-known/")
}

func (p *Project) reportUnread(m *yamlite.Map, reads, jsWords map[string]bool) {
	e := p.errs
	for i, k := range m.Keys {
		if !reads[m.Path+"\x00"+k] && !(strings.HasPrefix(m.Path, "data.") && jsWords[k]) {
			if file, rest, ok := strings.Cut(m.Path, "#params"); ok {
				e.add("%s:%d: front matter %s is never used by its layout; remove it or render it", file, m.Lines[i], strings.TrimPrefix(rest+"."+k, "."))
			} else if strings.HasPrefix(m.Path, "data.") {
				file, key := m.Path, ""
				for _, f := range p.dataFiles {
					if (m.Path == f || strings.HasPrefix(m.Path, f+".") || strings.HasPrefix(m.Path, f+"[")) && len(f) > len(key) {
						file, key = f, f
					}
				}
				rest := strings.TrimPrefix(strings.TrimPrefix(m.Path, key), ".")
				if rest != "" {
					rest += "."
				}
				loc := strings.ReplaceAll(file, ".", "/") + ".json"
				if i < len(m.Lines) && m.Lines[i] > 0 {
					loc += ":" + strconv.Itoa(m.Lines[i])
				}
				e.add("%s: key %s%s is never used by any template or script; remove it", loc, rest, k)
			} else if i < len(m.Lines) && m.Lines[i] > 0 {
				e.add("site.yml:%d: %s.%s is never used by any template; remove it", m.Lines[i], m.Path, k)
			} else {
				e.add("%s.%s: value is never used by any template", m.Path, k)
			}
			continue
		}
		var walk func(v any)
		walk = func(v any) {
			switch t := v.(type) {
			case *yamlite.Map:
				p.reportUnread(t, reads, jsWords)
			case []any:
				for _, c := range t {
					walk(c)
				}
			}
		}
		walk(m.Vals[i])
	}
}

type at struct {
	out  string
	file string
}

func (p *Project) report(w at, kind byte, match string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if w.out == "" {
		p.errs.add("%s: %s", w.file, msg)
		return
	}
	words := []string{match}
	if kind == findLink {
		words = linkWords(match)
	}
	p.errs.issue(pageIssue{out: w.out, off: -1, kind: kind, match: match, words: words, msg: msg})
}

func linkWords(l string) []string {
	w := []string{"(" + l, l}
	pth, frag, _ := strings.Cut(l, "#")
	if frag != "" {
		w = append(w, "#"+frag)
	}
	pth = strings.TrimSuffix(pth, "/")
	if i := strings.LastIndexByte(pth, '/'); i >= 0 && len(pth)-i > 4 {
		w = append(w, pth[i+1:])
	}
	return w
}

func (p *Project) internalTarget(base, link string, where at) (string, string, bool) {
	l := link
	low := strings.ToLower(l)
	switch {
	case strings.HasPrefix(low, "mailto:"), strings.HasPrefix(low, "tel:"), strings.HasPrefix(low, "sms:"), strings.HasPrefix(low, "data:"):
		return "", "", false
	case strings.HasPrefix(l, p.BaseURL+"/"):
		l = strings.TrimPrefix(l, p.BaseURL)
	case l == p.BaseURL:
		l = "/"
	case strings.HasPrefix(low, "https://"):
		return "", "", false
	case strings.HasPrefix(low, "http://"):
		p.report(where, findLink, link, "insecure link %s; use https://", link)
		return "", "", false
	case strings.HasPrefix(l, "//"):
		p.report(where, findLink, link, "protocol-relative link %s; write https://", link)
		return "", "", false
	case isExternal(l):
		p.report(where, findLink, link, "unsupported link scheme in %s; use https:, mailto:, tel: or a path", link)
		return "", "", false
	}
	pathPart, frag, _ := strings.Cut(l, "#")
	pathPart, _, _ = strings.Cut(pathPart, "?")
	if decoded, err := url.PathUnescape(frag); err == nil {
		frag = decoded
	}
	if pathPart == "" {
		return "", frag, true
	}
	if !strings.HasPrefix(pathPart, "/") {
		dir := base
		if !strings.HasSuffix(dir, "/") {
			dir = path.Dir(dir) + "/"
		}
		trailing := strings.HasSuffix(pathPart, "/")
		pathPart = path.Join(dir, pathPart)
		if trailing && pathPart != "/" {
			pathPart += "/"
		}
	}
	return pathPart, frag, true
}

func (p *Project) checkTarget(where at, link, target, frag string, files map[string]bool, records map[string]*OutRecord, redirects *redirectSet, linked map[string]bool) {
	if slices.Contains(p.endpoints, target) {
		return
	}
	if redirects != nil && redirects.sources[target] {
		p.report(where, findLink, link, "link %s goes through a redirect; link to %s instead", link, redirects.dest[target])
		return
	}
	file := strings.TrimPrefix(target, "/")
	if strings.HasSuffix(target, "/") {
		file += "index.html"
	}
	if !files[file] {
		if !strings.HasSuffix(target, "/") && files[file+"/index.html"] {
			p.report(where, findLink, link, "link %s must end with a slash (%s/)", link, target)
			return
		}
		p.report(where, findLink, link, "broken link %s: no page, file or endpoint at %s%s", link, target, p.nearestPath(target, files))
		return
	}
	linked[target] = true
	if frag == "" {
		return
	}
	r := records[file]
	if r == nil {
		return
	}
	if !slices.Contains(r.Scan.IDs, frag) {
		p.report(where, findLink, link, "link %s points to a missing #%s on %s%s", link, frag, target, suggest(frag, r.Scan.IDs))
	}
}

type redirectSet struct {
	lines   map[string]int
	sources map[string]bool
	dest    map[string]string
	targets map[string]int
}

func (p *Project) loadRedirects(files map[string]bool) *redirectSet {
	rs := &redirectSet{lines: map[string]int{}, sources: map[string]bool{}, dest: map[string]string{}, targets: map[string]int{}}
	f, err := os.Open(filepath.Join(p.Root, "static", "_redirects"))
	if err != nil {
		return rs
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if strings.HasPrefix(line, "#") {
			p.errs.add("static/_redirects:%d: comments are not allowed", n)
			continue
		}
		if len(fields) < 2 || len(fields) > 3 {
			p.errs.add("static/_redirects:%d: expected \"/from /to [status]\"", n)
			continue
		}
		from, to := fields[0], fields[1]
		if rs.sources[from] {
			p.errs.add("static/_redirects:%d: duplicate source %s", n, from)
		}
		rs.sources[from] = true
		rs.dest[from] = to
		rs.lines[from] = n
		if !strings.Contains(from, "*") && !strings.Contains(from, ":") {
			file := strings.TrimPrefix(from, "/")
			if strings.HasSuffix(from, "/") {
				file += "index.html"
			}
			if files[file] {
				p.errs.add("static/_redirects:%d: %s is redirected but the page still exists", n, from)
			}
		}
		if !strings.Contains(to, ":splat") && !strings.Contains(to, "*") {
			if _, ok := rs.targets[to]; !ok {
				rs.targets[to] = n
			}
		}
	}
	return rs
}

var importRE = regexp.MustCompile(`(?m)(?:import|export)\s[^;'"]*?from\s*["'](\.{1,2}/[^"']+)["']|import\s*\(\s*["'](\.{1,2}/[^"']+)["']`)

func (p *Project) workerScripts() []string {
	seen := map[string]bool{}
	var queue []string
	entries, _ := os.ReadDir(p.Root)
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".js") || strings.HasSuffix(e.Name(), ".mjs")) {
			queue = append(queue, filepath.Join(p.Root, e.Name()))
		}
	}
	var out []string
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if seen[f] {
			continue
		}
		seen[f] = true
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, f)
		for _, m := range importRE.FindAllStringSubmatch(string(b), -1) {
			rel := m[1] + m[2]
			if strings.HasSuffix(rel, ".js") || strings.HasSuffix(rel, ".mjs") {
				queue = append(queue, filepath.Join(filepath.Dir(f), rel))
			}
		}
	}
	return out
}

type sibling struct {
	base string
	root string
}

type siblingIndex struct {
	root     string
	prefixes []string
	codes    []string
	dirs     sync.Map
	allOnce  sync.Once
	allPaths []string
}

func (x *siblingIndex) all() []string {
	x.allOnce.Do(func() {
		if x.root == "" {
			return
		}
		set := map[string]bool{}
		for i, prefix := range x.prefixes {
			base := filepath.Join(x.root, "content", x.codes[i])
			filepath.WalkDir(base, func(full string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if d.Name() == "index.md" || d.Name() == "_index.md" {
					rel, _ := filepath.Rel(base, filepath.Dir(full))
					rel = filepath.ToSlash(rel)
					if rel == "." {
						set[prefix+"/"] = true
					} else {
						set[prefix+"/"+rel+"/"] = true
					}
				}
				return nil
			})
		}
		static := filepath.Join(x.root, "static")
		filepath.WalkDir(static, func(full string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(static, full)
				set["/"+filepath.ToSlash(rel)] = true
			}
			return nil
		})
		x.allPaths = keysOf(set)
	})
	return x.allPaths
}

func (x *siblingIndex) entries(dir string) map[string]bool {
	if v, ok := x.dirs.Load(dir); ok {
		return v.(map[string]bool)
	}
	m := map[string]bool{}
	list, _ := os.ReadDir(dir)
	for _, e := range list {
		m[e.Name()] = e.IsDir()
	}
	v, _ := x.dirs.LoadOrStore(dir, m)
	return v.(map[string]bool)
}

func (x *siblingIndex) dir(base, rel string) (string, bool) {
	if rel == "" {
		return base, true
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if isDir, ok := x.entries(base)[seg]; seg == "" || !ok || !isDir {
			return "", false
		}
		base = filepath.Join(base, seg)
	}
	return base, true
}

func (x *siblingIndex) file(base, rel string) bool {
	dir, name := "", rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		dir, name = rel[:i], rel[i+1:]
		if dir == "" {
			return false
		}
	}
	d, ok := x.dir(base, dir)
	if !ok {
		return false
	}
	isDir, ok := x.entries(d)[name]
	return ok && !isDir
}

func (x *siblingIndex) has(p string) bool {
	if x.root == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	if strings.HasSuffix(p, "/") {
		for i, prefix := range x.prefixes {
			rest, ok := strings.CutPrefix(p, prefix+"/")
			if !ok {
				continue
			}
			rel := strings.TrimSuffix(rest, "/")
			if rel == "" && rest != "" {
				continue
			}
			d, ok := x.dir(filepath.Join(x.root, "content", x.codes[i]), rel)
			if !ok {
				continue
			}
			es := x.entries(d)
			if isDir, ok := es["index.md"]; ok && !isDir {
				return true
			}
			if isDir, ok := es["_index.md"]; ok && !isDir {
				return true
			}
		}
		return false
	}
	return x.file(filepath.Join(x.root, "static"), p[1:])
}

func (p *Project) siblingURLs() map[string]*siblingIndex {
	out := map[string]*siblingIndex{}
	for _, sb := range p.siblings {
		x := &siblingIndex{}
		out[sb.base] = x
		raw, err := os.ReadFile(filepath.Join(sb.root, "site.yml"))
		if err != nil {
			p.errs.add("site.yml: sibling %s: %v", sb.base, err)
			continue
		}
		cfg, err := yamlite.Parse(filepath.Join(sb.root, "site.yml"), raw, 1)
		if err != nil {
			p.errs.add("site.yml: sibling %s: %v", sb.base, err)
			continue
		}
		langs, _ := cfg.Peek("languages")
		list, _ := langs.([]any)
		for i, l := range list {
			lm, ok := l.(*yamlite.Map)
			if !ok {
				continue
			}
			code, _ := lm.Peek("code")
			c, _ := code.(string)
			prefix := ""
			if i > 0 {
				prefix = "/" + c
			}
			x.prefixes = append(x.prefixes, prefix)
			x.codes = append(x.codes, c)
		}
		x.root = sb.root
	}
	return out
}

func (p *Project) checkSibling(link string, where at, urls map[string]*siblingIndex) bool {
	for base, set := range urls {
		if link != base && !strings.HasPrefix(link, base+"/") {
			continue
		}
		path := strings.TrimPrefix(link, base)
		path, _, _ = strings.Cut(path, "#")
		path, _, _ = strings.Cut(path, "?")
		if path == "" {
			path = "/"
		}
		if !set.has(path) {
			if !strings.HasSuffix(path, "/") && set.has(path+"/") {
				p.report(where, findLink, link, "link %s must end with a slash", link)
			} else {
				p.report(where, findLink, link, "broken link %s: no such page or file on %s%s", link, base, nearest(path, set.all()))
			}
		}
		return true
	}
	return false
}

func (p *Project) checkSelfLinks(pg *Page, r *OutRecord, out, head string) {
	self := p.BaseURL + pg.RelPermalink
	if len(r.Scan.Canon) > 1 {
		p.errs.at(head, fmt.Sprintf("page has %d canonical links; keep one", len(r.Scan.Canon)), pg)
	}
	for _, c := range r.Scan.Canon {
		if c != self {
			p.report(at{out: out}, findLink, c, "canonical link %s is not this page's URL %s", c, self)
		}
	}
	if r.Scan.OGURL != "" && r.Scan.OGURL != self {
		p.report(at{out: out}, findLink, r.Scan.OGURL, "og:url %s is not this page's URL %s", r.Scan.OGURL, self)
	}
	if len(r.Scan.Alts) == 0 {
		return
	}
	own := map[string]bool{self: true}
	for _, t := range pg.Translations {
		own[p.BaseURL+t.RelPermalink] = true
	}
	hasSelf := false
	for _, a := range r.Scan.Alts {
		lang, href, _ := strings.Cut(a, " ")
		switch {
		case !own[href] && (href == p.BaseURL || strings.HasPrefix(href, p.BaseURL+"/")):
			p.report(at{out: out}, findLink, href, "hreflang %s link %s is neither this page nor one of its translations", lang, href)
		case href == self && lang != "x-default":
			hasSelf = true
		}
	}
	if !hasSelf {
		p.errs.at(head, "hreflang links do not include the page itself; list every translation including this one", pg)
	}
}
