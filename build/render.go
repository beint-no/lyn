package build

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/bits"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/beint-no/lyn/yamlite"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
)

type recorder struct {
	metaSet    []uint64
	metas      []int32
	bodies     []int32
	colls      []int32
	rcolls     []int32
	paths      []string
	i18n       []int32
	reads      []uint64
	res        []int32
	shortcodes []string
	cards      []int32
	year       bool
}

func (r *recorder) meta(p *Page) {
	if p.idx >= 0 {
		r.addMeta(p.idx)
	}
}

func (r *recorder) addMeta(i int32) {
	w := int(i >> 6)
	if w >= len(r.metaSet) {
		r.metaSet = append(r.metaSet, make([]uint64, w+1-len(r.metaSet))...)
	}
	bit := uint64(1) << (uint(i) & 63)
	if r.metaSet[w]&bit != 0 {
		return
	}
	r.metaSet[w] |= bit
	r.metas = append(r.metas, i)
}

func (r *recorder) body(p *Page) {
	if p.idx < 0 {
		return
	}
	if n := len(r.bodies); n > 0 && r.bodies[n-1] == p.idx {
		return
	}
	r.bodies = append(r.bodies, p.idx)
}

func (r *recorder) coll(p *Page) {
	if n := len(r.colls); n > 0 && r.colls[n-1] == p.idx {
		return
	}
	r.colls = append(r.colls, p.idx)
}

func (r *recorder) rcoll(p *Page) {
	if n := len(r.rcolls); n > 0 && r.rcolls[n-1] == p.idx {
		return
	}
	r.rcolls = append(r.rcolls, p.idx)
}

func (r *recorder) path(key string) { r.paths = append(r.paths, key) }

func (r *recorder) merge(o *recorder) {
	if o == nil {
		return
	}
	for _, i := range o.metas {
		r.addMeta(i)
	}
	r.bodies = append(r.bodies, o.bodies...)
	r.colls = append(r.colls, o.colls...)
	r.rcolls = append(r.rcolls, o.rcolls...)
	r.paths = append(r.paths, o.paths...)
	r.i18n = append(r.i18n, o.i18n...)
	r.reads = append(r.reads, o.reads...)
	r.res = append(r.res, o.res...)
	r.shortcodes = append(r.shortcodes, o.shortcodes...)
	r.cards = append(r.cards, o.cards...)
	r.year = r.year || o.year
}

func compactSorted[T interface{ ~int32 | ~uint64 | ~string }](s []T) []T {
	slices.Sort(s)
	return slices.Clip(slices.Compact(s))
}

func (r *recorder) compact() {
	r.metas = r.metas[:0]
	for w, word := range r.metaSet {
		for word != 0 {
			b := bits.TrailingZeros64(word)
			r.metas = append(r.metas, int32(w<<6+b))
			word &= word - 1
		}
	}
	r.bodies = compactSorted(r.bodies)
	r.colls = compactSorted(r.colls)
	r.rcolls = compactSorted(r.rcolls)
	r.paths = compactSorted(r.paths)
	r.i18n = compactSorted(r.i18n)
	r.reads = compactSorted(r.reads)
	r.res = compactSorted(r.res)
	r.shortcodes = compactSorted(r.shortcodes)
	r.cards = compactSorted(r.cards)
}

func (r *rctx) read(m *yamlite.Map, i int) {
	if m.ID >= 0 {
		r.rec.reads = append(r.rec.reads, uint64(m.ID)<<32|uint64(i))
	}
}

func (r *rctx) useResource(res *Resource) {
	if res.card {
		r.rec.cards = append(r.rec.cards, res.page.idx)
		return
	}
	if n := len(r.rec.res); n > 0 && r.rec.res[n-1] == res.idx {
		return
	}
	r.rec.res = append(r.rec.res, res.idx)
}

func (r *rctx) resourceURL(res *Resource) string {
	r.useResource(res)
	if res.asset {
		if err := r.p.ensureAsset(res); err != nil {
			panic(&tplError{res.src, err.Error()})
		}
	}
	return res.RelPermalink
}

func (p *Project) ensureAsset(res *Resource) error {
	res.procOnce.Do(func() { p.processAsset(res) })
	return res.procErr
}

var minifier = func() *minify.M {
	m := minify.New()
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("application/javascript", js.Minify)
	return m
}()

func (p *Project) processAsset(res *Resource) {
	src, err := os.ReadFile(filepath.Join(p.Root, res.src))
	if err != nil {
		res.procErr = err
		return
	}
	ext := path.Ext(res.Name)
	base := strings.TrimSuffix(res.Name, ext)
	res.data = src
	if ext == ".css" || ext == ".js" {
		mt := "text/css"
		if ext == ".js" {
			mt = "application/javascript"
		}
		res.data, err = p.minify(mt, src)
		if err != nil {
			res.procErr = fmt.Errorf("minify: %v", err)
			return
		}
		base += ".min"
	}
	sum := sha256.Sum256(res.data)
	res.outName = base + "." + hex.EncodeToString(sum[:8]) + ext
	res.RelPermalink = "/" + res.outName
	res.Integrity = "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	res.processed = true
}

func normalizePagePath(from *Page, p string) string {
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		if from == nil {
			p = "/" + p
		} else {
			p = path.Join(from.Path, p)
		}
	}
	p = path.Clean(p)
	if p != "/" {
		p += "/"
	}
	return p
}

func (r *rctx) getPage(s *Site, from *Page, raw, loc string) any {
	key := normalizePagePath(from, raw)
	r.rec.path(s.Language.Code + ":" + key)
	if pg := s.byPath[key]; pg != nil {
		return pg
	}
	return nil
}

func (r *rctx) link(raw, loc string) string {
	if isExternal(raw) {
		fail(loc, "link is for internal paths; write external URLs directly")
	}
	pathPart, suffix := raw, ""
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		pathPart, suffix = raw[:i], raw[i:]
	}
	if !strings.HasPrefix(pathPart, "/") {
		fail(loc, "link needs a path starting with /, got %q", raw)
	}
	key := normalizePagePath(nil, pathPart)
	r.rec.path(r.site.Language.Code + ":" + key)
	if pg := r.site.byPath[key]; pg != nil {
		r.rec.meta(pg)
		return pg.RelPermalink + suffix
	}
	if r.p.outputExists(pathPart) {
		return pathPart + suffix
	}
	fail(loc, "link %q: no %s page, static file or endpoint at that path", raw, r.site.Language.Code)
	return ""
}

type output struct {
	page   *Page
	path   string
	layout *tmpl
}

func (p *Project) layoutFor(pg *Page) (*tmpl, error) {
	name := pg.Layout
	if name == "" {
		switch pg.Kind {
		case "home":
			name = "home"
		case "section":
			name = "section"
		case "404":
			name = "404"
		default:
			name = "page"
		}
	}
	l := p.tpl.layouts[name]
	if l == nil {
		if pg.Layout != "" {
			return nil, fmt.Errorf("%s: layout %q does not exist (layouts/%s.html)%s", p.fmLoc(pg, "layout"), name, name, suggest(name, keysOf(p.tpl.layouts)))
		}
		return nil, fmt.Errorf("%s: needs layouts/%s.html", orStr(pg.File, pg.RelPermalink), name)
	}
	if !l.compiled {
		return nil, fmt.Errorf("layouts/%s.html failed to compile", name)
	}
	return l, nil
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func outPath(pg *Page) string {
	if pg.Kind == "404" {
		return strings.TrimPrefix(pg.RelPermalink, "/")
	}
	return strings.TrimPrefix(pg.RelPermalink, "/") + "index.html"
}

func (p *Project) render(o *output) (html []byte, rec *recorder, err error) {
	rc := &rctx{p: p, page: o.page, site: o.page.site, rec: &recorder{}}
	defer func() {
		if x := recover(); x != nil {
			if e, ok := x.(*tplError); ok {
				err = e
				return
			}
			panic(x)
		}
	}()
	s := &st{r: rc, dot: o.page, vars: make([]any, o.layout.nvars), out: getBuf(32 << 10)}
	s.vars[0] = o.page
	rc.rec.meta(o.page)
	o.layout.root(s)
	rc.rec.compact()
	return s.out, rc.rec, nil
}

func (p *Project) renderFailed(pg *Page, err error) {
	e, ok := err.(*tplError)
	if !ok {
		p.errs.add("%v", err)
		return
	}
	if e.loc != "" {
		p.errs.at(e.loc, e.msg, pg)
		return
	}
	for line := range strings.SplitSeq(e.msg, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			loc, msg := splitLoc(line)
			p.errs.at(loc, msg, pg)
		}
	}
}
