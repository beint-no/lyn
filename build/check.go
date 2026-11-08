package build

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/beint-no/lyn/yamlite"
)

var markdownClasses = map[string]bool{"footnotes": true, "footnote-ref": true, "footnote-backref": true}

func (p *Project) check(ns *State, files map[string]bool) {
	e := p.errs
	records := ns.Outputs
	outKeys := sortedKeys(records)

	redirects := p.loadRedirects(files)

	jsText := &bytes.Buffer{}
	for _, a := range p.assets {
		if strings.HasSuffix(a.Name, ".js") {
			jsText.Write(a.data)
			jsText.WriteByte('\n')
		}
	}
	for _, f := range p.static {
		if strings.HasSuffix(f, ".js") {
			b, _ := os.ReadFile(filepath.Join(p.Root, "static", f))
			jsText.Write(b)
			jsText.WriteByte('\n')
		}
	}
	for _, f := range p.workerScripts() {
		b, _ := os.ReadFile(f)
		jsText.Write(b)
		jsText.WriteByte('\n')
	}
	jsWords := map[string]bool{}
	for _, w := range wordRE.FindAll(jsText.Bytes(), -1) {
		jsWords[string(w)] = true
	}
	for _, r := range records {
		for _, w := range r.Scan.Words {
			jsWords[w] = true
		}
	}

	linked := map[string]bool{}
	titles := map[string]string{}
	usedBodies := map[string]bool{}
	usedI18n := map[string]bool{}
	reads := map[string]bool{}
	usedRes := map[string]bool{}
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

	for _, out := range outKeys {
		r := records[out]
		pg := pageByKey[r.Page]
		url := "/" + strings.TrimSuffix(out, "index.html")
		where := url
		if pg != nil && pg.File != "" {
			where = pg.File
		}
		for _, msg := range r.Scan.Errs {
			e.add("%s: %s", where, msg)
		}
		if pg != nil {
			if l, err := p.layoutFor(pg); err == nil {
				usedLayouts[l.name] = true
			}
		}
		if r.Scan.Titles != 1 {
			e.add("%s: page must have exactly one <title> (found %d)", where, r.Scan.Titles)
		} else if r.Scan.Title == "" {
			e.add("%s: <title> is empty", where)
		} else if pg != nil && pg.Kind != "404" {
			tkey := pg.Lang.Code + "\x00" + r.Scan.Title
			if other, ok := titles[tkey]; ok {
				e.add("%s: duplicate <title> %q (also on %s)", where, r.Scan.Title, other)
			} else {
				titles[tkey] = where
			}
		}
		if pg != nil && pg.Kind != "404" {
			if !pg.NoIndex && strings.TrimSpace(r.Scan.Desc) == "" {
				e.add("%s: indexable page has no <meta name=\"description\">", where)
			}
			if pg.NoIndex && !r.Scan.NoIndex {
				e.add("%s: noindex page must render <meta name=\"robots\" content=\"noindex\">", where)
			}
		}
		for _, b := range r.Bodies {
			usedBodies[b] = true
		}
		for _, k := range r.I18n {
			usedI18n[k] = true
		}
		for _, k := range r.Reads {
			reads[k] = true
		}
		for _, k := range r.Res {
			usedRes[k] = true
		}
		for _, c := range r.Scan.Classes {
			if _, ok := classes[c]; !ok {
				classes[c] = where
			}
		}
		for _, d := range r.Scan.Data {
			if _, ok := dataAttrs[d]; !ok {
				dataAttrs[d] = where
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
		where string
	}
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
							e.add("%s: link to #%s, but this page has no element with that id", j.where, frag)
						}
						continue
					}
					p.checkTarget(j.where, l, target, frag, files, records, redirects, local)
				}
			}
		})
	}
	for _, out := range outKeys {
		r := records[out]
		where := "/" + strings.TrimSuffix(out, "index.html")
		if pg := pageByKey[r.Page]; pg != nil && pg.File != "" {
			where = pg.File
		}
		jobs <- linkJob{r, "/" + strings.TrimSuffix(out, "index.html"), where}
	}
	close(jobs)
	wg.Wait()
	for _, part := range linkedParts {
		for k := range part {
			linked[k] = true
		}
	}
	for to := range redirects.targets {
		target, frag, ok := p.internalTarget("/", to, "static/_redirects")
		if ok && target != "" {
			p.checkTarget("static/_redirects", to, target, frag, files, records, nil, linked)
		}
	}

	for _, pg := range p.pages {
		if len(bytes.TrimSpace(pg.body)) > 0 && !usedBodies[pageKey(pg)] {
			e.add("%s: the page body is never rendered by its layout; remove it or render .Content", pg.File)
		}
	}
	for _, k := range p.i18nKeys {
		if !usedI18n[k] {
			e.add("%s: translation %q is never used", p.i18nLines[k], k)
		}
	}
	for _, root := range p.mapRoots {
		reportUnread(root, reads, jsWords, e)
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
			e.add("site.yml: pageParams.%s is declared but no page sets it", k)
		}
	}
	for _, k := range sortedKeys(p.toolParams) {
		if !setParams[k] {
			e.add("site.yml: toolParams.%s is declared but no page sets it", k)
		}
	}
	for name, l := range p.tpl.layouts {
		if !usedLayouts[name] {
			e.add("%s: layout is never used by any page", l.file)
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
			e.add("%s: shortcode is never used", sc.file)
		}
	}
	for _, pt := range p.tpl.partials {
		if !reach[pt] {
			e.add("%s: partial is never used", pt.file)
		}
	}
	for _, res := range p.resources {
		if !usedRes[resKey(res)] && !linked[res.RelPermalink] {
			if res.asset {
				e.add("assets/%s: asset is never used", res.Name)
			} else {
				e.add("%s: bundle file is never used", res.src)
			}
		}
	}

	css := newCSSInfo()
	for _, a := range p.assets {
		if strings.HasSuffix(a.Name, ".css") && a.data != nil {
			if err := css.analyze("assets/"+a.Name, a.data); err != nil {
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
			target = path.Join("/", path.Dir(strings.TrimPrefix(file, "assets/")), u)
		}
		if isExternal(target) {
			continue
		}
		t, _, _ := strings.Cut(target, "?")
		t, _, _ = strings.Cut(t, "#")
		if !files[strings.TrimPrefix(t, "/")] {
			e.add("%s: url(%s) does not exist", file, u)
		}
		linked[t] = true
	}
	for c, file := range css.classes {
		if _, ok := classes[c]; !ok && !jsWords[c] {
			e.add("%s: CSS class .%s is never used", file, c)
		}
	}
	for c, where := range classes {
		if _, ok := css.classes[c]; !ok && !jsWords[c] && !markdownClasses[c] && !strings.HasPrefix(c, "language-") {
			e.add("%s: class %q has no CSS rule", where, c)
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
			e.add("%s: custom property %s is never read", file, v)
		}
	}
	for v, file := range varBare {
		if _, ok := varDefs[v]; !ok && !strings.Contains(js, v) {
			e.add("%s: var(%s) has no fallback and %s is never defined", file, v, v)
		}
	}
	for d, where := range dataAttrs {
		camel := dataCamel(d)
		if !css.attrs[d] && !strings.Contains(js, d) && !jsWords[camel] && !jsWords[d] {
			e.add("%s: attribute %s is never used by CSS or scripts", where, d)
		}
	}
	for _, f := range p.static {
		if p.mustBeLinked(f) && !linked["/"+f] {
			e.add("static/%s: file is never referenced", f)
		}
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

func reportUnread(m *yamlite.Map, reads, jsWords map[string]bool, e *errorList) {
	for i, k := range m.Keys {
		if !reads[m.Path+"\x00"+k] && !(strings.HasPrefix(m.Path, "data.") && jsWords[k]) {
			if file, rest, ok := strings.Cut(m.Path, "#params"); ok {
				e.add("%s:%d: front matter %s%s is never used by its layout", file, m.Lines[i], strings.TrimPrefix(rest+"."+k, "."), "")
			} else {
				e.add("%s.%s: value is never used by any template", m.Path, k)
			}
			continue
		}
		var walk func(v any)
		walk = func(v any) {
			switch t := v.(type) {
			case *yamlite.Map:
				reportUnread(t, reads, jsWords, e)
			case []any:
				for _, c := range t {
					walk(c)
				}
			}
		}
		walk(m.Vals[i])
	}
}

func (p *Project) internalTarget(base, link, where string) (string, string, bool) {
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
		p.errs.add("%s: insecure link %s; use https://", where, link)
		return "", "", false
	case strings.HasPrefix(l, "//"):
		p.errs.add("%s: protocol-relative link %s; write https://", where, link)
		return "", "", false
	case isExternal(l):
		p.errs.add("%s: unsupported link %s", where, link)
		return "", "", false
	}
	pathPart, frag, _ := strings.Cut(l, "#")
	pathPart, _, _ = strings.Cut(pathPart, "?")
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

func (p *Project) checkTarget(where, link, target, frag string, files map[string]bool, records map[string]*OutRecord, redirects *redirectSet, linked map[string]bool) {
	if slices.Contains(p.endpoints, target) {
		return
	}
	if redirects != nil && redirects.sources[target] {
		p.errs.add("%s: link %s goes through a redirect; link to %s instead", where, link, redirects.dest[target])
		return
	}
	file := strings.TrimPrefix(target, "/")
	if strings.HasSuffix(target, "/") {
		file += "index.html"
	}
	if !files[file] {
		if !strings.HasSuffix(target, "/") && files[file+"/index.html"] {
			p.errs.add("%s: link %s must end with a slash (%s/)", where, link, target)
			return
		}
		p.errs.add("%s: broken link %s", where, link)
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
		p.errs.add("%s: link %s points to a missing #%s on %s", where, link, frag, target)
	}
}

type redirectSet struct {
	sources map[string]bool
	dest    map[string]string
	targets map[string]bool
}

func (p *Project) loadRedirects(files map[string]bool) *redirectSet {
	rs := &redirectSet{sources: map[string]bool{}, dest: map[string]string{}, targets: map[string]bool{}}
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
			rs.targets[to] = true
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
