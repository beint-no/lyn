package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template/parse"
	"unsafe"
)

type srcMark struct {
	off  int
	text *textSrc
	loc  string
	page *Page
	key  string
}

type textSrc struct {
	tree   *parse.Tree
	n      int
	pieces []textPiece
}

type textPiece struct {
	off  int
	node *parse.TextNode
}

type tracer struct {
	html map[*byte][]srcMark
	src  map[*byte]srcMark
	fm   map[*byte]srcMark
}

func (t *tracer) action(s *st, v any, mode outMode, loc string) {
	off := len(s.out)
	var ptr *byte
	switch x := v.(type) {
	case HTML:
		if len(x) > 0 {
			ptr = unsafe.StringData(string(x))
		}
		if mode == outText && ptr != nil {
			if marks, ok := t.html[ptr]; ok {
				s.marks = append(s.marks, srcMark{off: off, loc: loc})
				for _, m := range marks {
					m.off += off
					s.marks = append(s.marks, m)
				}
				return
			}
		}
	case string:
		if len(x) > 0 {
			ptr = unsafe.StringData(x)
		}
	}
	if ptr != nil {
		m, ok := t.src[ptr]
		if !ok {
			m, ok = t.fm[ptr]
		}
		if ok {
			m.off = off
			s.marks = append(s.marks, m)
			return
		}
	}
	s.marks = append(s.marks, srcMark{off: off, loc: loc})
}

func (t *tracer) source(v string, pg *Page, key string) {
	if len(v) > 0 {
		t.src[unsafe.StringData(v)] = srcMark{page: pg, key: key}
	}
}

func (p *Project) newTracer() *tracer {
	p.fmOnce.Do(func() {
		p.fmSrc = map[*byte]srcMark{}
		for _, pg := range p.pages {
			for key, v := range map[string]string{"title": pg.Title, "seoTitle": pg.SeoTitle, "description": pg.Description} {
				if len(v) > 0 {
					p.fmSrc[unsafe.StringData(v)] = srcMark{page: pg, key: key}
				}
			}
		}
	})
	return &tracer{html: map[*byte][]srcMark{}, src: map[*byte]srcMark{}, fm: p.fmSrc}
}

func (p *Project) traceRender(o *output) (html []byte, marks []srcMark, ok bool) {
	rc := &rctx{p: p, page: o.page, site: o.page.site, rec: &recorder{}, tr: p.newTracer()}
	defer func() {
		if x := recover(); x != nil {
			if _, isTpl := x.(*tplError); !isTpl {
				panic(x)
			}
			ok = false
		}
	}()
	s := &st{r: rc, dot: o.page, vars: make([]any, o.layout.nvars), out: make([]byte, 0, 32<<10)}
	s.vars[0] = o.page
	o.layout.root(s)
	return s.out, s.marks, true
}

func (p *Project) resolveIssues() {
	if len(p.errs.issues) == 0 {
		return
	}
	byOut := map[string][]pageIssue{}
	for _, is := range p.errs.issues {
		byOut[is.out] = append(byOut[is.out], is)
	}
	p.errs.issues = nil
	byMsg := map[string]map[string]bool{}
	outs := sortedKeys(byOut)
	for _, out := range outs {
		for _, is := range byOut[out] {
			byMsg[is.msg] = map[string]bool{}
		}
	}
	const sampleSize = 24
	traced := map[string]bool{}
	var mu sync.Mutex
	run := func(list []string) {
		jobs := make(chan string)
		var wg sync.WaitGroup
		for range runtime.GOMAXPROCS(0) {
			wg.Go(func() {
				for out := range jobs {
					locs := p.resolvePage(out, byOut[out])
					mu.Lock()
					for i, is := range byOut[out] {
						byMsg[is.msg][locs[i]] = true
					}
					mu.Unlock()
				}
			})
		}
		for _, out := range list {
			jobs <- out
		}
		close(jobs)
		wg.Wait()
	}
	var first []string
	count := map[string]int{}
	for _, out := range outs {
		need := false
		for _, is := range byOut[out] {
			if count[is.msg] < sampleSize {
				need = true
			}
		}
		if need {
			for _, is := range byOut[out] {
				count[is.msg]++
			}
			first = append(first, out)
			traced[out] = true
		}
	}
	run(first)
	var rest []string
	for _, out := range outs {
		if traced[out] {
			continue
		}
		for _, is := range byOut[out] {
			if !sharedTemplateLoc(byMsg[is.msg]) {
				rest = append(rest, out)
				traced[out] = true
				break
			}
		}
	}
	run(rest)
	for _, out := range outs {
		if traced[out] {
			continue
		}
		for _, is := range byOut[out] {
			for loc := range byMsg[is.msg] {
				p.errs.at(loc, is.msg, p.outputs[out].page)
			}
		}
	}
}

func sharedTemplateLoc(locs map[string]bool) bool {
	if len(locs) != 1 {
		return false
	}
	for l := range locs {
		return strings.HasPrefix(l, "layouts/")
	}
	return false
}

func (p *Project) resolvePage(out string, issues []pageIssue) []string {
	locs := make([]string, len(issues))
	o := p.outputs[out]
	if o == nil {
		for i, is := range issues {
			locs[i] = "/" + strings.TrimSuffix(out, "index.html")
			p.errs.at(locs[i], is.msg, nil)
		}
		return locs
	}
	html, marks, ok := p.traceRender(o)
	for i, is := range issues {
		loc := orStr(o.page.File, o.page.RelPermalink)
		if ok {
			off := is.off
			if off < 0 && is.kind != findNone {
				off = locate(html, is.kind, is.match)
			}
			if off >= 0 && off <= len(html) {
				if l := p.sourceAt(html, marks, off, is); l != "" {
					loc = l
				}
			}
		}
		locs[i] = loc
		p.errs.at(loc, is.msg, o.page)
	}
	return locs
}

func (p *Project) sourceAt(html []byte, marks []srcMark, off int, is pageIssue) string {
	i := sort.Search(len(marks), func(i int) bool { return marks[i].off > off }) - 1
	if i < 0 {
		return ""
	}
	m := marks[i]
	switch {
	case m.page != nil && m.key != "":
		if n := p.frontMatterLine(m.page, m.key); n > 0 {
			return fmt.Sprintf("%s:%d", m.page.File, n)
		}
		return m.page.File
	case m.page != nil:
		if n := markdownLine(m.page, is.words); n > 0 {
			return fmt.Sprintf("%s:%d", m.page.File, n)
		}
		return m.page.File
	case m.text != nil:
		return m.text.lineAt(html[m.off:min(len(html), m.off+m.text.n)], off-m.off)
	}
	return m.loc
}

func (t *textSrc) lineAt(chunk []byte, rel int) string {
	k := max(sort.Search(len(t.pieces), func(i int) bool { return t.pieces[i].off > rel })-1, 0)
	pc := t.pieces[k]
	end := len(chunk)
	if k+1 < len(t.pieces) {
		end = min(end, t.pieces[k+1].off)
	}
	loc, _ := t.tree.ErrorContext(pc.node)
	file, line, _ := locParts(loc)
	if line == 0 {
		return loc
	}
	processed := chunk[min(pc.off, end):end]
	line += alignNewlines(pc.node.Text, processed, rel-pc.off)
	return file + ":" + strconv.Itoa(line)
}

func alignNewlines(raw, out []byte, n int) int {
	i := 0
	for k := 0; k < len(out) && k <= n; k++ {
		for i < len(raw) && raw[i] != out[k] && !(out[k] == ' ' && isSpace(raw[i])) {
			i++
		}
		if k == n || i >= len(raw) {
			break
		}
		if out[k] == ' ' && isSpace(raw[i]) {
			for i < len(raw) && isSpace(raw[i]) {
				i++
			}
		} else {
			i++
		}
	}
	return bytes.Count(raw[:min(i, len(raw))], []byte("\n"))
}

func markdownLine(pg *Page, words []string) int {
	body := pg.body
	at := func(i int) int { return pg.bodyLine + bytes.Count(body[:i], []byte("\n")) }
	for _, w := range words {
		if len(w) >= 2 {
			if i := bytes.Index(body, []byte(w)); i >= 0 {
				return at(i)
			}
		}
	}
	var tokens []string
	for _, w := range words {
		for f := range strings.FieldsSeq(w) {
			if len(f) >= 4 && !slices.Contains(tokens, f) {
				tokens = append(tokens, f)
			}
		}
	}
	if len(tokens) == 0 {
		return 0
	}
	best, bestScore := 0, 0
	for n, line := range bytes.Split(body, []byte("\n")) {
		score := 0
		for _, t := range tokens {
			if bytes.Contains(line, []byte(t)) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = n, score
		}
	}
	if bestScore < min(2, len(tokens)) {
		return 0
	}
	return pg.bodyLine + best
}

func (p *Project) frontMatterLine(pg *Page, key string) int {
	b, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(pg.File)))
	if err != nil {
		return 0
	}
	for n, line := range bytes.Split(b, []byte("\n")) {
		if n > 0 && bytes.Equal(bytes.TrimSpace(line), []byte("---")) {
			return 0
		}
		if bytes.HasPrefix(line, []byte(key+":")) {
			return n + 1
		}
	}
	return 0
}

func locate(html []byte, kind byte, match string) int {
	found := -1
	walkTags(html, func(name string, start, end int, attrs []attr) bool {
		switch kind {
		case findTag:
			if name == match {
				found = start
			}
		default:
			for _, a := range attrs {
				hit := false
				switch kind {
				case findLink:
					switch a.name {
					case "href", "src", "action", "poster", "content":
						hit = a.val == match
					case "srcset":
						hit = strings.Contains(a.val, match)
					}
				case findClass:
					hit = a.name == "class" && slices.Contains(strings.Fields(a.val), match)
				case findAttr:
					hit = a.name == match
				}
				if hit {
					found = start
					if i := bytes.Index(html[start:end], []byte(" "+a.name+"=")); i >= 0 {
						found = start + i + 1
					}
					break
				}
			}
		}
		return found < 0
	})
	return found
}

func walkTags(b []byte, fn func(name string, start, end int, attrs []attr) bool) {
	i := 0
	for i < len(b) {
		j := bytes.IndexByte(b[i:], '<')
		if j < 0 {
			return
		}
		i += j
		if bytes.HasPrefix(b[i:], []byte("<!--")) {
			end := bytes.Index(b[i:], []byte("-->"))
			if end < 0 {
				return
			}
			i += end + 3
			continue
		}
		k := i + 1
		for k < len(b) && isNameByte(b[k]) {
			k++
		}
		if k == i+1 {
			i++
			continue
		}
		name := strings.ToLower(string(b[i+1 : k]))
		attrs, end, _, ok := parseAttrs(b, k)
		if !ok {
			return
		}
		if !fn(name, i, end, attrs) {
			return
		}
		i = end
		if name == "script" || name == "style" {
			if c := indexFold(b[i:], []byte("</"+name)); c >= 0 {
				i += c
			}
		}
	}
}
