package build

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/beint-no/lyn/yamlite"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var mdStateKey = parser.NewContextKey()

type mdState struct {
	r      *rctx
	page   *Page
	errs   []string
	images int
	loc    string
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.DefinitionList, extension.Footnote, extension.Typographer),
	goldmark.WithParserOptions(parser.WithAttribute(), parser.WithASTTransformers(util.Prioritized(linkTransformer{}, 100))),
	goldmark.WithRendererOptions(html.WithUnsafe(), renderer.WithNodeRenderers(util.Prioritized(imageRenderer{}, 100))),
)

type headingIDs struct {
	seen map[string]int
}

func (h *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	id := headingID(string(value))
	if id == "" {
		id = "heading"
	}
	if n, ok := h.seen[id]; ok {
		h.seen[id] = n + 1
		id = id + "-" + strconv.Itoa(n+1)
	} else {
		h.seen[id] = 0
	}
	return []byte(id)
}

func (h *headingIDs) Put(value []byte) { h.seen[string(value)] = 0 }

type linkTransformer struct{}

func (linkTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	ms, _ := pc.Get(mdStateKey).(*mdState)
	if ms == nil {
		return
	}
	ids := &headingIDs{seen: map[string]int{}}
	src := reader.Source()
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if id, ok := n.AttributeString("id"); ok {
				ids.Put(id.([]byte))
			}
		}
		return ast.WalkContinue, nil
	})
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch x := n.(type) {
		case *ast.Heading:
			if _, ok := x.AttributeString("id"); !ok {
				x.SetAttributeString("id", ids.Generate([]byte(plainText(x, src)), ast.KindHeading))
			}
		case *ast.Link:
			dest := string(x.Destination)
			if out, err := ms.r.resolveLink(ms.page, dest); err != nil {
				ms.errs = append(ms.errs, err.Error())
			} else {
				x.Destination = []byte(out)
			}
		case *ast.AutoLink:
		case *ast.Image:
			dest := string(x.Destination)
			if out, err := ms.r.resolveImage(ms.page, dest); err != nil {
				ms.errs = append(ms.errs, err.Error())
			} else {
				x.Destination = []byte(out)
				ms.size(x)
			}
		}
		return ast.WalkContinue, nil
	})
}

type imageRenderer struct{}

func (imageRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, renderImage)
}

func renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	alt := plainText(n, source)
	w.WriteString(`<img src="`)
	w.Write(escapeHTML(nil, string(n.Destination)))
	w.WriteString(`" alt="`)
	w.Write(escapeHTML(nil, alt))
	w.WriteByte('"')
	if n.Title != nil {
		w.WriteString(` title="`)
		w.Write(escapeHTML(nil, string(n.Title)))
		w.WriteByte('"')
	}
	if v, ok := n.AttributeString("width"); ok {
		fmt.Fprintf(w, ` width="%v" height="%v"`, string(v.([]byte)), func() string { h, _ := n.AttributeString("height"); return string(h.([]byte)) }())
	}
	if v, ok := n.AttributeString("loading"); ok {
		fmt.Fprintf(w, ` loading="%s" decoding="async"`, v.([]byte))
	}
	w.WriteByte('>')
	return ast.WalkSkipChildren, nil
}

func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
		case *ast.String:
			b.WriteString(stdhtml.UnescapeString(string(t.Value)))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func headingID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

var shortcodeRE = regexp.MustCompile(`\{\{<\s*(/?)\s*([a-z][a-z0-9-]*)((?:\s+[^>]*?)?)\s*>\}\}`)

type scCall struct {
	name  string
	args  string
	inner string
	loc   string
}

func (r *rctx) content(p *Page, loc string) HTML {
	if r.rendering == p {
		fail(loc, "%s reads its own content (Content, TableOfContents, WordCount, ReadingTime or HasFragment) while that content is being rendered, e.g. from a shortcode", p.File)
	}
	p.contentOnce.Do(func() {
		cr := &rctx{p: r.p, page: p, site: p.site, rec: &recorder{}, rendering: p}
		html, err := cr.renderMarkdown(p)
		if err != nil {
			p.contentErr = err
		}
		p.content = html
		p.contentRec = cr.rec
		p.contentRec.compact()
	})
	if p.contentErr != nil {
		panic(&tplError{p.File, p.contentErr.Error()})
	}
	r.rec.merge(p.contentRec)
	return p.content
}

func (r *rctx) renderMarkdown(p *Page) (HTML, error) {
	body := p.body
	var calls []scCall
	if bytes.Contains(body, []byte("{{")) {
		var err error
		body, calls, err = extractShortcodes(p, body)
		if err != nil {
			return "", err
		}
	}
	ms := &mdState{r: r, page: p}
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{seen: map[string]int{}}))
	ctx.Set(mdStateKey, ms)
	reader := text.NewReader(body)
	doc := md.Parser().Parse(reader, parser.WithContext(ctx))
	if len(ms.errs) > 0 {
		return "", fmt.Errorf("%s", strings.Join(ms.errs, "\n  "))
	}
	var buf bytes.Buffer
	buf.Grow(len(body) * 2)
	bw := getWriter(&buf)
	err := md.Renderer().Render(bw, body, doc)
	putWriter(bw)
	if err != nil {
		return "", err
	}
	out := wrapTables(buf.Bytes())
	p.ids, p.toc = headings(doc, body), toc(doc, body)
	if len(calls) > 0 {
		var err error
		out, err = r.expandShortcodes(p, out, calls)
		if err != nil {
			return "", err
		}
	}
	return HTML(out), nil
}

func extractShortcodes(p *Page, body []byte) ([]byte, []scCall, error) {
	var calls []scCall
	var out bytes.Buffer
	matches := shortcodeRE.FindAllSubmatchIndex(body, -1)
	last := 0
	for i := 0; i < len(matches); i++ {
		m := matches[i]
		lineNo := p.bodyLine + bytes.Count(body[:m[0]], []byte("\n"))
		loc := fmt.Sprintf("%s:%d", p.File, lineNo)
		if m[3] > m[2] {
			return nil, nil, fmt.Errorf("%s: closing shortcode without opening", loc)
		}
		name := string(body[m[4]:m[5]])
		args := strings.TrimSpace(string(body[m[6]:m[7]]))
		closing := false
		args, closing = strings.CutSuffix(args, "/")
		call := scCall{name: name, args: strings.TrimSpace(args), loc: loc}
		end := m[1]
		if !closing {
			for j := i + 1; j < len(matches); j++ {
				n := matches[j]
				if n[3] > n[2] && string(body[n[4]:n[5]]) == name {
					call.inner = string(body[m[1]:n[0]])
					end = n[1]
					i = j
					break
				}
			}
		}
		out.Write(body[last:m[0]])
		fmt.Fprintf(&out, "LYNSC%dX", len(calls))
		calls = append(calls, call)
		last = end
	}
	out.Write(body[last:])
	if bytes.Contains(out.Bytes(), []byte("{{<")) || bytes.Contains(out.Bytes(), []byte("{{%")) {
		return nil, nil, fmt.Errorf("%s: malformed shortcode; use {{< name key=\"value\" >}}", p.File)
	}
	return out.Bytes(), calls, nil
}

var scArgRE = regexp.MustCompile(`^(?:([a-zA-Z][a-zA-Z0-9_]*)=)?"((?:[^"\\]|\\.)*)"\s*`)

func (r *rctx) expandShortcodes(p *Page, html []byte, calls []scCall) ([]byte, error) {
	for i, c := range calls {
		sc := r.p.tpl.shortcodes[c.name]
		if sc == nil || !sc.compiled {
			return nil, fmt.Errorf("%s: shortcode %q does not exist in layouts/shortcodes/", c.loc, c.name)
		}
		ctx := &shortcodeCtx{page: p, named: yamlite.NewMap()}
		ctx.named.ID = -2
		args := c.args
		for args != "" {
			m := scArgRE.FindStringSubmatch(args)
			if m == nil {
				return nil, fmt.Errorf("%s: shortcode arguments must be key=\"value\" or \"value\"", c.loc)
			}
			v, err := strconv.Unquote(`"` + m[2] + `"`)
			if err != nil {
				return nil, fmt.Errorf("%s: bad shortcode argument: %v", c.loc, err)
			}
			if m[1] != "" {
				ctx.named.Set(m[1], v, 0)
			} else {
				ctx.positional = append(ctx.positional, v)
			}
			args = args[len(m[0]):]
		}
		if c.inner != "" {
			inner, err := r.renderFragment(p, []byte(c.inner))
			if err != nil {
				return nil, fmt.Errorf("%s: %v", c.loc, err)
			}
			ctx.inner = inner
		}
		out, err := r.execShortcode(sc, ctx)
		if err != nil {
			return nil, err
		}
		r.rec.shortcodes = append(r.rec.shortcodes, c.name)
		token := []byte(fmt.Sprintf("LYNSC%dX", i))
		para := append(append([]byte("<p>"), token...), "</p>"...)
		if bytes.Contains(html, para) {
			html = bytes.Replace(html, para, out, 1)
		} else if bytes.Contains(html, token) {
			html = bytes.Replace(html, token, out, 1)
		} else {
			return nil, fmt.Errorf("%s: shortcode %q ended up inside a code span or link", c.loc, c.name)
		}
	}
	return html, nil
}

func (r *rctx) execShortcode(sc *tmpl, ctx *shortcodeCtx) (out []byte, err error) {
	defer func() {
		if x := recover(); x != nil {
			if e, ok := x.(*tplError); ok {
				err = e
				return
			}
			panic(x)
		}
	}()
	s := &st{r: r, dot: ctx, vars: make([]any, sc.nvars)}
	s.vars[0] = ctx
	sc.root(s)
	return s.out, nil
}

func (r *rctx) renderFragment(p *Page, src []byte) (HTML, error) {
	ms := &mdState{r: r, page: p}
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{seen: map[string]int{}}))
	ctx.Set(mdStateKey, ms)
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(ctx))
	if len(ms.errs) > 0 {
		return "", fmt.Errorf("%s", strings.Join(ms.errs, "; "))
	}
	var buf bytes.Buffer
	bw := getWriter(&buf)
	err := md.Renderer().Render(bw, src, doc)
	putWriter(bw)
	if err != nil {
		return "", err
	}
	return HTML(wrapTables(buf.Bytes())), nil
}

func (r *rctx) markdownify(src, loc string) HTML {
	p := r.page
	if p == nil {
		fail(loc, "markdownify needs a current page")
	}
	h, err := r.renderFragment(p, []byte(src))
	if err != nil {
		fail(loc, "markdownify: %v", err)
	}
	s := strings.TrimSpace(string(h))
	if strings.HasPrefix(s, "<p>") && strings.HasSuffix(s, "</p>") && strings.Count(s, "<p>") == 1 {
		s = s[3 : len(s)-4]
	}
	return HTML(s)
}

func headings(doc ast.Node, src []byte) []string {
	var ids []string
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if id, ok := n.AttributeString("id"); ok {
				ids = append(ids, string(id.([]byte)))
			}
		}
		return ast.WalkContinue, nil
	})
	return ids
}

func toc(doc ast.Node, src []byte) HTML {
	var b strings.Builder
	level := 0
	open := 0
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok || h.Level < 2 || h.Level > 3 {
			return ast.WalkContinue, nil
		}
		id, _ := h.AttributeString("id")
		switch {
		case level == 0:
			b.WriteString("<ul>")
			open++
		case h.Level > level:
			b.WriteString("<ul>")
			open++
		case h.Level < level && open > 1:
			b.WriteString("</li></ul></li>")
			open--
		default:
			b.WriteString("</li>")
		}
		level = h.Level
		b.WriteString(`<li><a href="#`)
		b.Write(escapeHTML(nil, string(id.([]byte))))
		b.WriteString(`">`)
		b.Write(escapeText(nil, plainText(h, src)))
		b.WriteString("</a>")
		return ast.WalkSkipChildren, nil
	})
	if open == 0 {
		return ""
	}
	for ; open > 0; open-- {
		b.WriteString("</li></ul>")
	}
	return HTML(`<nav id="TableOfContents">` + b.String() + `</nav>`)
}

func isExternal(dest string) bool {
	if strings.HasPrefix(dest, "//") {
		return true
	}
	i := strings.IndexAny(dest, ":/?#")
	return i > 0 && dest[i] == ':'
}

func (r *rctx) resolveLink(p *Page, dest string) (string, error) {
	where := p.File
	if dest == "" {
		return "", fmt.Errorf("%s: empty link", where)
	}
	if isExternal(dest) {
		lower := strings.ToLower(dest)
		switch {
		case strings.HasPrefix(lower, "http://"):
			return "", fmt.Errorf("%s: insecure link %s; use https://", where, dest)
		case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "mailto:"), strings.HasPrefix(lower, "tel:"):
		default:
			return "", fmt.Errorf("%s: unsupported link scheme in %s", where, dest)
		}
		if strings.HasPrefix(dest, r.p.BaseURL+"/") || dest == r.p.BaseURL {
			return "", fmt.Errorf("%s: link %s points to this site; use the path %s", where, dest, strings.TrimPrefix(dest, r.p.BaseURL))
		}
		return dest, nil
	}
	if strings.HasPrefix(dest, "#") {
		return dest, nil
	}
	u, err := url.Parse(dest)
	if err != nil {
		return "", fmt.Errorf("%s: invalid link %q", where, dest)
	}
	target := u.Path
	if !strings.HasPrefix(target, "/") {
		target = path.Join(p.RelPermalink, target)
	}
	if strings.HasSuffix(u.Path, "/") && !strings.HasSuffix(target, "/") {
		target += "/"
	}
	suffix := ""
	if u.RawQuery != "" {
		suffix += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		suffix += "#" + u.Fragment
	}
	if pg := r.findPage(p.site, target); pg != nil {
		return pg.RelPermalink + suffix, nil
	}
	if r.p.outputExists(target) {
		return target + suffix, nil
	}
	if res := r.findResource(p, u.Path); res != nil {
		r.useResource(res)
		return res.RelPermalink + suffix, nil
	}
	return "", fmt.Errorf("%s: broken link %q (no page, static file or endpoint at %s)", where, dest, target)
}

func (r *rctx) findResource(p *Page, rel string) *Resource {
	if strings.HasPrefix(rel, "/") {
		return nil
	}
	rel = path.Clean(rel)
	for _, res := range p.Resources {
		if res.Name == rel {
			return res
		}
	}
	return nil
}

func (r *rctx) resolveImage(p *Page, dest string) (string, error) {
	if isExternal(dest) {
		if !strings.HasPrefix(strings.ToLower(dest), "https://") {
			return "", fmt.Errorf("%s: image %s must use https://", p.File, dest)
		}
		return dest, nil
	}
	if res := r.findResource(p, dest); res != nil {
		r.useResource(res)
		return res.RelPermalink, nil
	}
	if strings.HasPrefix(dest, "/") && r.p.staticExists(dest) {
		return dest, nil
	}
	return "", fmt.Errorf("%s: image %q does not exist in the page bundle or static/", p.File, dest)
}

func (p *Project) staticExists(urlPath string) bool {
	_, ok := p.staticSet()[strings.TrimPrefix(urlPath, "/")]
	return ok
}

func (p *Project) staticSet() map[string]struct{} {
	p.staticOnce.Do(func() {
		p.staticIdx = make(map[string]struct{}, len(p.static))
		for _, s := range p.static {
			p.staticIdx[s] = struct{}{}
		}
	})
	return p.staticIdx
}

func (p *Project) outputExists(target string) bool {
	if p.staticExists(target) {
		return true
	}
	for _, e := range p.endpoints {
		if e == target {
			return true
		}
	}
	return false
}

func (r *rctx) findPage(s *Site, target string) *Page {
	if pg := s.byPath[target]; pg != nil {
		return pg
	}
	if !strings.HasSuffix(target, "/") {
		if pg := s.byPath[target+"/"]; pg != nil {
			return pg
		}
	}
	for _, other := range r.p.Sites {
		if other.Language.Prefix == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(target, other.Language.Prefix+"/"); ok {
			return r.findPage(other, "/"+rest)
		}
	}
	return nil
}

func (r *rctx) imageSize(res *Resource) (int, int) {
	res.sizeOnce.Do(func() {
		data := r.resourceData(res)
		if res.MediaType == "image/svg+xml" {
			res.w, res.h = svgSize(data)
			return
		}
		res.w, res.h = rasterSize(data)
	})
	return res.w, res.h
}

var svgTagRE = regexp.MustCompile(`(?s)<svg\b[^>]*>`)
var svgAttrRE = regexp.MustCompile(`\s(width|height|viewBox)="([^"]*)"`)

func svgSize(data []byte) (int, int) {
	tag := svgTagRE.Find(data)
	var w, h float64
	var vb []float64
	for _, m := range svgAttrRE.FindAllSubmatch(tag, -1) {
		v := strings.TrimSuffix(string(m[2]), "px")
		switch string(m[1]) {
		case "width":
			w, _ = strconv.ParseFloat(v, 64)
		case "height":
			h, _ = strconv.ParseFloat(v, 64)
		case "viewBox":
			for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' }) {
				x, _ := strconv.ParseFloat(f, 64)
				vb = append(vb, x)
			}
		}
	}
	if (w == 0 || h == 0) && len(vb) == 4 {
		w, h = vb[2], vb[3]
	}
	return int(w + 0.5), int(h + 0.5)
}

func (r *rctx) resourceData(res *Resource) []byte {
	b, err := r.p.resourceBytes(res)
	if err != nil {
		panic(&tplError{res.src, err.Error()})
	}
	return b
}

func (p *Project) resourceBytes(res *Resource) ([]byte, error) {
	if res.asset {
		if err := p.ensureAsset(res); err != nil {
			return nil, err
		}
		return res.data, nil
	}
	res.mu.Lock()
	defer res.mu.Unlock()
	if res.data == nil {
		b, err := os.ReadFile(filepath.Join(p.Root, res.src))
		if err != nil {
			return nil, err
		}
		res.data = b
	}
	return res.data, nil
}

func (ms *mdState) size(img *ast.Image) {
	ms.images++
	dest := string(img.Destination)
	var w, h int
	if res := ms.findByURL(dest); res != nil {
		w, h = ms.r.imageSize(res)
	} else if !isExternal(dest) {
		w, h = ms.r.staticImageSize(dest)
	}
	if w > 0 && h > 0 {
		img.SetAttributeString("width", []byte(strconv.Itoa(w)))
		img.SetAttributeString("height", []byte(strconv.Itoa(h)))
	}
	if ms.images > 1 {
		img.SetAttributeString("loading", []byte("lazy"))
	}
}

func (ms *mdState) findByURL(u string) *Resource {
	for _, x := range ms.page.Resources {
		if x.RelPermalink == u {
			return x
		}
	}
	return nil
}

func (r *rctx) staticImageSize(urlPath string) (int, int) {
	data, err := os.ReadFile(filepath.Join(r.p.Root, "static", filepath.FromSlash(strings.TrimPrefix(urlPath, "/"))))
	if err != nil {
		return 0, 0
	}
	if strings.HasSuffix(urlPath, ".svg") {
		return svgSize(data)
	}
	return rasterSize(data)
}

func wrapTables(b []byte) []byte {
	if bytes.Contains(b, []byte("<pre>")) {
		b = bytes.ReplaceAll(b, []byte("<pre>"), []byte(`<pre tabindex="0">`))
	}
	if !bytes.Contains(b, []byte("<table")) {
		return b
	}
	b = bytes.ReplaceAll(b, []byte("<table"), []byte(`<div class="table-scroll"><table`))
	return bytes.ReplaceAll(b, []byte("</table>"), []byte("</table></div>"))
}

var scriptStyleRE = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
var tagNameRE = regexp.MustCompile(`^</?([a-zA-Z][a-zA-Z0-9]*)`)

func stripHTML(s string) string {
	s = scriptStyleRE.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllStringFunc(s, func(tag string) string {
		m := tagNameRE.FindStringSubmatch(tag)
		if m != nil && (blockTags[strings.ToLower(m[1])] || strings.EqualFold(m[1], "li") || strings.EqualFold(m[1], "td") || strings.EqualFold(m[1], "th")) {
			return " "
		}
		return ""
	})
	return stdhtml.UnescapeString(s)
}

func (p *Page) wordCount() int {
	p.wordsOnce.Do(func() {
		n, in := 0, false
		for _, c := range stripHTML(string(p.content)) {
			if unicode.IsSpace(c) {
				in = false
			} else if !in {
				in = true
				n++
			}
		}
		p.words = n
	})
	return p.words
}

func (r *rctx) summary(p *Page, loc string) HTML {
	if p.Summary == "" {
		return ""
	}
	p.summaryOnce.Do(func() {
		cr := &rctx{p: r.p, page: p, site: p.site, rec: &recorder{}}
		h, err := cr.renderFragment(p, []byte(inlineOnly(p.Summary)))
		if err != nil {
			p.summaryErr = err
			return
		}
		out := strings.TrimSpace(string(h))
		if strings.HasPrefix(out, "<p>") && strings.HasSuffix(out, "</p>") && strings.Count(out, "<p>") == 1 {
			out = out[3 : len(out)-4]
		}
		p.summaryHTML = HTML(out)
		p.summaryRec = cr.rec
	})
	if p.summaryErr != nil {
		panic(&tplError{p.File, "summary: " + p.summaryErr.Error()})
	}
	r.rec.merge(p.summaryRec)
	return p.summaryHTML
}

var blockStartRE = regexp.MustCompile(`^(\d+)([.)])(\s|$)`)

func inlineOnly(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if m := blockStartRE.FindStringSubmatchIndex(s); m != nil {
		return s[:m[3]] + "\\" + s[m[3]:]
	}
	for _, marker := range []string{"- ", "+ ", "* ", "#", ">", "```", "~~~"} {
		if strings.HasPrefix(s, marker) {
			return "\\" + s
		}
	}
	return s
}
