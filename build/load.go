package build

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/beint-no/lyn/yamlite"
	"github.com/zeebo/xxh3"
)

type Project struct {
	Root         string
	Out          string
	BaseURL      string
	Title        string
	Languages    []*Language
	Sites        []*Site
	pages        Pages
	params       *yamlite.Map
	data         *yamlite.Map
	i18n         map[string]map[string]string
	i18nKeys     []string
	i18nIDs      map[string]int
	i18nLines    map[string]string
	maps         []*yamlite.Map
	assets       map[string]*Resource
	resources    []*Resource
	static       []string
	endpoints    []string
	tpl          *templates
	errs         *errorList
	files        map[string]fileInfo
	contentFiles []string
	configHash   uint64
	mapRoots     []*yamlite.Map
	staticStamps []string
	outExisted   bool
	cache        *ContentCache
	hints        map[string]bool
	staticOnce   sync.Once
	staticIdx    map[string]struct{}
}

type fileInfo struct {
	size  int64
	mtime int64
	hash  uint64
}

type errorList struct {
	mu   sync.Mutex
	errs []string
}

func (e *errorList) add(format string, args ...any) {
	e.mu.Lock()
	e.errs = append(e.errs, fmt.Sprintf(format, args...))
	e.mu.Unlock()
}

func (e *errorList) err() error {
	if len(e.errs) == 0 {
		return nil
	}
	slices.Sort(e.errs)
	e.errs = slices.Compact(e.errs)
	return fmt.Errorf("%d problem(s):\n  %s", len(e.errs), strings.Join(e.errs, "\n  "))
}

var segmentRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var resourceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func readYAML(p *Project, rel string) (*yamlite.Map, error) {
	b, err := os.ReadFile(filepath.Join(p.Root, rel))
	if err != nil {
		return nil, err
	}
	return yamlite.Parse(rel, b, 1)
}

func (p *Project) trackMap(m *yamlite.Map, path string) {
	p.mapRoots = append(p.mapRoots, m)
	yamlite.Walk(m, func(x *yamlite.Map) {
		x.ID = int32(len(p.maps))
		x.Track()
		p.maps = append(p.maps, x)
	})
	m.Path = path
	var setPaths func(v any, prefix string)
	setPaths = func(v any, prefix string) {
		switch t := v.(type) {
		case *yamlite.Map:
			t.Path = prefix
			for i, k := range t.Keys {
				setPaths(t.Vals[i], prefix+"."+k)
			}
		case []any:
			for i, c := range t {
				setPaths(c, fmt.Sprintf("%s[%d]", prefix, i))
			}
		}
	}
	setPaths(m, path)
}

func expectKeys(m *yamlite.Map, file string, allowed ...string) error {
	for i, k := range m.Keys {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("%s:%d: unknown key %q (allowed: %s)", file, m.Lines[i], k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func str(m *yamlite.Map, key, file string, required bool) (string, error) {
	v, ok := m.Peek(key)
	if !ok {
		if required {
			return "", fmt.Errorf("%s: missing required key %q", file, key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s:%d: %q must be a string", file, m.Lines[m.Find(key)], key)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s:%d: %q must not be empty", file, m.Lines[m.Find(key)], key)
	}
	return s, nil
}

func Load(root string) (*Project, error) {
	return LoadWith(root, &ContentCache{}, nil)
}

func LoadWith(root string, cache *ContentCache, hints map[string]bool) (*Project, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	p := &Project{Root: root, Out: filepath.Join(root, "public"), errs: &errorList{}, i18nIDs: map[string]int{}, i18nLines: map[string]string{}, cache: cache, hints: hints}
	if err := p.loadConfig(); err != nil {
		return nil, err
	}
	if err := p.loadData(); err != nil {
		return nil, err
	}
	if err := p.loadI18n(); err != nil {
		return nil, err
	}
	if err := p.loadContent(); err != nil {
		return nil, err
	}
	if err := p.loadAssets(); err != nil {
		return nil, err
	}
	if err := p.loadStatic(); err != nil {
		return nil, err
	}
	if err := p.buildMenus(); err != nil {
		return nil, err
	}
	if err := p.loadTemplates(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Project) loadConfig() error {
	const file = "site.yml"
	raw, err := os.ReadFile(filepath.Join(p.Root, file))
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	p.configHash = xxh3.Hash(raw)
	m, err := yamlite.Parse(file, raw, 1)
	if err != nil {
		return err
	}
	if err := expectKeys(m, file, "baseURL", "title", "languages", "params", "endpoints"); err != nil {
		return err
	}
	if p.BaseURL, err = str(m, "baseURL", file, true); err != nil {
		return err
	}
	if !strings.HasPrefix(p.BaseURL, "https://") || !strings.HasSuffix(p.BaseURL, "/") || strings.Count(p.BaseURL, "/") != 3 {
		return fmt.Errorf("%s: baseURL must look like https://example.com/", file)
	}
	p.BaseURL = strings.TrimSuffix(p.BaseURL, "/")
	if p.Title, err = str(m, "title", file, true); err != nil {
		return err
	}
	if v, ok := m.Peek("params"); ok {
		pm, ok := v.(*yamlite.Map)
		if !ok {
			return fmt.Errorf("%s: params must be a mapping", file)
		}
		p.params = pm
	} else {
		p.params = yamlite.NewMap()
	}
	p.trackMap(p.params, "params")
	if v, ok := m.Peek("endpoints"); ok {
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: endpoints must be a list of paths", file)
		}
		for _, e := range list {
			s, ok := e.(string)
			if !ok || !strings.HasPrefix(s, "/") {
				return fmt.Errorf("%s: endpoint %v must be a path starting with /", file, e)
			}
			p.endpoints = append(p.endpoints, s)
		}
	}
	lv, ok := m.Peek("languages")
	list, isList := lv.([]any)
	if !ok || !isList || len(list) == 0 {
		return fmt.Errorf("%s: languages must be a non-empty list", file)
	}
	for i, item := range list {
		lm, ok := item.(*yamlite.Map)
		if !ok {
			return fmt.Errorf("%s: languages[%d] must be a mapping", file, i)
		}
		if err := expectKeys(lm, file, "code", "locale", "label", "title", "menus"); err != nil {
			return err
		}
		l := &Language{Weight: i}
		if l.Code, err = str(lm, "code", file, true); err != nil {
			return err
		}
		if !segmentRE.MatchString(l.Code) {
			return fmt.Errorf("%s: language code %q must be lowercase ASCII", file, l.Code)
		}
		if l.Locale, err = str(lm, "locale", file, true); err != nil {
			return err
		}
		if l.Label, err = str(lm, "label", file, len(list) > 1); err != nil {
			return err
		}
		if l.Title, err = str(lm, "title", file, false); err != nil {
			return err
		}
		if l.Title == "" {
			l.Title = p.Title
		}
		if i > 0 {
			l.Prefix = "/" + l.Code
		}
		site := &Site{Title: l.Title, BaseURL: p.BaseURL, Language: l, project: p, byPath: map[string]*Page{}}
		l.site = site
		site.Params = p.params
		menus := yamlite.NewMap()
		if v, ok := lm.Peek("menus"); ok {
			mm, ok := v.(*yamlite.Map)
			if !ok {
				return fmt.Errorf("%s: languages[%d].menus must be a mapping", file, i)
			}
			menus = mm
		}
		site.Menus = menus
		p.Languages = append(p.Languages, l)
		p.Sites = append(p.Sites, site)
	}
	return nil
}

func (p *Project) loadData() error {
	p.data = yamlite.NewMap()
	dir := filepath.Join(p.Root, "data")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yml") {
			return fmt.Errorf("data/%s: data files must be .yml files directly in data/", name)
		}
		m, err := readYAML(p, filepath.Join("data", name))
		if err != nil {
			return err
		}
		key := strings.TrimSuffix(name, ".yml")
		p.trackMap(m, "data."+key)
		p.data.Set(key, m, 0)
	}
	return nil
}

func (p *Project) loadI18n() error {
	p.i18n = map[string]map[string]string{}
	keys := map[string][]string{}
	for _, l := range p.Languages {
		rel := filepath.Join("i18n", l.Code+".yml")
		m, err := readYAML(p, rel)
		if errors.Is(err, fs.ErrNotExist) {
			if len(p.Languages) > 1 {
				return fmt.Errorf("%s is missing; every language needs a translation file", rel)
			}
			p.i18n[l.Code] = map[string]string{}
			continue
		}
		if err != nil {
			return err
		}
		t := map[string]string{}
		for i, k := range m.Keys {
			s, ok := m.Vals[i].(string)
			if !ok {
				return fmt.Errorf("%s:%d: translation %q must be a string", rel, m.Lines[i], k)
			}
			if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(k) {
				return fmt.Errorf("%s:%d: translation key %q must be snake_case", rel, m.Lines[i], k)
			}
			t[k] = s
			keys[k] = append(keys[k], l.Code)
			if _, ok := p.i18nIDs[k]; !ok {
				p.i18nIDs[k] = len(p.i18nKeys)
				p.i18nKeys = append(p.i18nKeys, k)
				p.i18nLines[k] = fmt.Sprintf("%s:%d", rel, m.Lines[i])
			}
		}
		p.i18n[l.Code] = t
	}
	for k, langs := range keys {
		if len(langs) != len(p.Languages) {
			var missing []string
			for _, l := range p.Languages {
				if !slices.Contains(langs, l.Code) {
					missing = append(missing, l.Code)
				}
			}
			p.errs.add("i18n: key %q is missing in %s", k, strings.Join(missing, ", "))
		}
	}
	return nil
}

type rawPage struct {
	lang   *Language
	dir    string
	file   string
	branch bool
	src    []byte
	res    []resFile
}

type resFile struct {
	name  string
	stamp string
}

type langScan struct {
	bundles map[string]*rawPage
	files   []string
	stamps  map[string]string
	errs    []string
}

type ContentCache struct {
	scans map[string]*langScan
}

func (p *Project) scanLang(l *Language, base string) (*langScan, error) {
	sc := &langScan{bundles: map[string]*rawPage{}, stamps: map[string]string{}}
	var mu sync.Mutex
	errf := func(format string, args ...any) {
		mu.Lock()
		sc.errs = append(sc.errs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	err := walkParallel(base, func(rel string, d fs.DirEntry) error {
		name := d.Name()
		if d.IsDir() {
			if !segmentRE.MatchString(name) {
				errf("content/%s/%s: folder names must be lowercase ASCII words separated by hyphens", l.Code, rel)
			}
			return nil
		}
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		switch name {
		case "index.md", "_index.md":
			src, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			rp := &rawPage{lang: l, dir: dir, file: path.Join("content", l.Code, rel), branch: name == "_index.md", src: src}
			mu.Lock()
			if prev, dup := sc.bundles[dir]; dup {
				sc.errs = append(sc.errs, fmt.Sprintf("%s and %s: a folder has either index.md or _index.md, not both", prev.file, rp.file))
			}
			sc.bundles[dir] = rp
			mu.Unlock()
		default:
			if strings.HasSuffix(name, ".md") {
				errf("content/%s/%s: content must be index.md (page) or _index.md (section) inside its own folder", l.Code, rel)
				return nil
			}
			stamp := ""
			if fi, err := d.Info(); err == nil {
				stamp = fileStamp(fi)
			}
			mu.Lock()
			sc.files = append(sc.files, rel)
			sc.stamps[rel] = stamp
			mu.Unlock()
		}
		return nil
	})
	slices.Sort(sc.files)
	return sc, err
}

func fileStamp(fi fs.FileInfo) string {
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

func (p *Project) applyHints(old *langScan, l *Language, base string) *langScan {
	sc := &langScan{bundles: make(map[string]*rawPage, len(old.bundles)), files: old.files, stamps: old.stamps, errs: old.errs}
	for k, v := range old.bundles {
		sc.bundles[k] = v
	}
	copied := false
	for abs := range p.hints {
		rel, err := filepath.Rel(base, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		name := path.Base(rel)
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		if name == "index.md" || name == "_index.md" {
			rp, ok := sc.bundles[dir]
			if !ok || rp.file != path.Join("content", l.Code, rel) {
				return nil
			}
			src, err := os.ReadFile(abs)
			if err != nil {
				return nil
			}
			n := *rp
			n.src = src
			sc.bundles[dir] = &n
			continue
		}
		if _, ok := sc.stamps[rel]; !ok {
			return nil
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil
		}
		if !copied {
			sc.stamps = make(map[string]string, len(old.stamps))
			for k, v := range old.stamps {
				sc.stamps[k] = v
			}
			copied = true
		}
		sc.stamps[rel] = fileStamp(fi)
	}
	return sc
}

func (p *Project) loadContent() error {
	var raws []*rawPage
	if p.cache.scans == nil {
		p.cache.scans = map[string]*langScan{}
	}
	for _, l := range p.Languages {
		base := filepath.Join(p.Root, "content", l.Code)
		var sc *langScan
		if old := p.cache.scans[l.Code]; old != nil && p.hints != nil {
			sc = p.applyHints(old, l, base)
		}
		if sc == nil {
			if _, err := os.Stat(base); err != nil {
				return fmt.Errorf("content/%s/ is missing", l.Code)
			}
			var err error
			sc, err = p.scanLang(l, base)
			if err != nil {
				return err
			}
		}
		p.cache.scans[l.Code] = sc
		for _, e := range sc.errs {
			p.errs.add("%s", e)
		}
		bundles := make(map[string]*rawPage, len(sc.bundles))
		for k, v := range sc.bundles {
			n := *v
			n.lang = l
			bundles[k] = &n
		}
		files, stamps := sc.files, sc.stamps
		for _, f := range files {
			dir := path.Dir(f)
			if dir == "." {
				dir = ""
			}
			owner := ""
			found := false
			for d := dir; ; d = parentDir(d) {
				if b, ok := bundles[d]; ok {
					if !b.branch || d == dir {
						owner, found = d, true
					}
					break
				}
				if d == "" {
					break
				}
			}
			if !found {
				p.errs.add("content/%s/%s: file does not belong to a page bundle", l.Code, f)
				continue
			}
			b := bundles[owner]
			resName := strings.TrimPrefix(strings.TrimPrefix(f, owner), "/")
			for _, seg := range strings.Split(resName, "/") {
				if !resourceRE.MatchString(seg) {
					p.errs.add("content/%s/%s: file names must be ASCII without spaces", l.Code, f)
				}
			}
			b.res = append(b.res, resFile{resName, stamps[f]})
		}
		if _, ok := bundles[""]; !ok {
			return fmt.Errorf("content/%s/_index.md (the home page) is missing", l.Code)
		}
		for dir, b := range bundles {
			if dir == "" {
				continue
			}
			parent := parentDir(dir)
			pb, ok := bundles[parent]
			for !ok {
				p.errs.add("content/%s/%s/: folder has pages but no _index.md, so /%s/ would be a 404", l.Code, parent, parent)
				parent = parentDir(parent)
				pb, ok = bundles[parent]
			}
			if !pb.branch {
				p.errs.add("%s: a page bundle (index.md) cannot contain other pages; make it a section with _index.md", pb.file)
			}
			raws = append(raws, b)
		}
		raws = append(raws, bundles[""])
	}
	slices.SortFunc(raws, func(a, b *rawPage) int {
		return cmp.Or(cmp.Compare(a.lang.Weight, b.lang.Weight), cmp.Compare(a.dir, b.dir))
	})
	pages := make(Pages, len(raws))
	var wg sync.WaitGroup
	work := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range work {
				pg, err := p.parsePage(raws[i])
				if err != nil {
					p.errs.add("%v", err)
					continue
				}
				pages[i] = pg
			}
		})
	}
	for i := range raws {
		work <- i
	}
	close(work)
	wg.Wait()
	if err := p.errs.err(); err != nil {
		return err
	}
	p.pages = pages
	for i, pg := range pages {
		pg.idx = int32(i)
		s := pg.Lang.site
		pg.site = s
		s.byPath[pg.Path] = pg
		s.Pages = append(s.Pages, pg)
	}
	for _, s := range p.Sites {
		s.Home = s.byPath["/"]
		for _, pg := range s.Pages {
			if pg.Path == "/" {
				continue
			}
			parent := s.byPath[parentPath(pg.Path)]
			pg.Parent = parent
			parent.Children = append(parent.Children, pg)
		}
		for _, pg := range s.Pages {
			sortPages(pg.Children)
			if pg.Kind == "section" || pg.Kind == "home" {
				s.sections = append(s.sections, pg)
			}
		}
		sortPages(s.Pages)
	}
	byPath := map[string]Pages{}
	for _, pg := range pages {
		byPath[pg.Path] = append(byPath[pg.Path], pg)
	}
	for _, group := range byPath {
		if len(group) < 2 {
			continue
		}
		for _, pg := range group {
			for _, o := range group {
				if o != pg {
					pg.Translations = append(pg.Translations, o)
				}
			}
		}
	}
	for _, s := range p.Sites {
		nf := &Page{idx: -1, Kind: "404", Lang: s.Language, Path: "/404.html", RelPermalink: s.Language.Prefix + "/404.html", Title: "404", site: s, Params: yamlite.NewMap(), NoIndex: true}
		nf.Parent = s.Home
		s.notFound = nf
	}
	return nil
}

func parentDir(d string) string {
	i := strings.LastIndexByte(d, '/')
	if i < 0 {
		return ""
	}
	return d[:i]
}

func parentPath(p string) string {
	t := strings.TrimSuffix(p, "/")
	i := strings.LastIndexByte(t, '/')
	return t[:i+1]
}

func sortPages(ps Pages) {
	if len(ps) < 2 {
		return
	}
	titles := make([]string, len(ps))
	for i, p := range ps {
		titles[i] = p.Title
	}
	keys := collationKeys(ps[0].Lang.Locale, titles)
	byPage := make(map[*Page][]byte, len(ps))
	for i, p := range ps {
		byPage[p] = keys[i]
	}
	slices.SortStableFunc(ps, func(a, b *Page) int {
		wa, wb := a.Weight, b.Weight
		if wa == 0 {
			wa = 1 << 30
		}
		if wb == 0 {
			wb = 1 << 30
		}
		return cmp.Or(cmp.Compare(wa, wb), b.Date.Compare(a.Date), bytes.Compare(byPage[a], byPage[b]), cmp.Compare(a.Path, b.Path))
	})
}

var pageKeys = []string{"title", "seoTitle", "description", "summary", "layout", "date", "lastmod", "weight", "noindex", "params"}

func (p *Project) parsePage(r *rawPage) (*Page, error) {
	src := r.src
	fmRaw, body, ok := yamlite.SplitFrontMatter(src)
	if !ok {
		return nil, fmt.Errorf("%s: front matter (--- ... ---) is required", r.file)
	}
	m, err := yamlite.Parse(r.file, fmRaw, 2)
	if err != nil {
		return nil, err
	}
	if err := expectKeys(m, r.file, pageKeys...); err != nil {
		return nil, err
	}
	pg := &Page{Lang: r.lang, File: r.file, Dir: r.dir, body: body, bodyLine: bytes.Count(src[:len(src)-len(body)], []byte("\n")) + 1}
	pg.metaHash = xxh3.Hash(fmRaw) ^ xxh3.HashString(r.file)
	pg.bodyHash = xxh3.Hash(body)
	switch {
	case r.dir == "":
		pg.Kind, pg.Path = "home", "/"
	case r.branch:
		pg.Kind, pg.Path = "section", "/"+r.dir+"/"
	default:
		pg.Kind, pg.Path = "page", "/"+r.dir+"/"
	}
	pg.RelPermalink = r.lang.Prefix + pg.Path
	if pg.Title, err = str(m, "title", r.file, true); err != nil {
		return nil, err
	}
	if pg.SeoTitle, err = str(m, "seoTitle", r.file, false); err != nil {
		return nil, err
	}
	if pg.Description, err = str(m, "description", r.file, true); err != nil {
		return nil, err
	}
	if pg.Summary, err = str(m, "summary", r.file, false); err != nil {
		return nil, err
	}
	if pg.Layout, err = str(m, "layout", r.file, false); err != nil {
		return nil, err
	}
	for _, k := range []string{"date", "lastmod"} {
		s, err := str(m, k, r.file, false)
		if err != nil {
			return nil, err
		}
		if s == "" {
			continue
		}
		t, err := parseDate(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %s %q must be YYYY-MM-DD or RFC 3339", r.file, k, s)
		}
		if k == "date" {
			pg.Date = t
		} else {
			pg.Lastmod = t
		}
	}
	if pg.Lastmod.IsZero() {
		pg.Lastmod = pg.Date
	}
	if v, ok := m.Peek("weight"); ok {
		w, ok := v.(int)
		if !ok || w <= 0 {
			return nil, fmt.Errorf("%s: weight must be a positive integer", r.file)
		}
		pg.Weight = w
	}
	if v, ok := m.Peek("noindex"); ok {
		b, ok := v.(bool)
		if !ok || !b {
			return nil, fmt.Errorf("%s: noindex must be true when present", r.file)
		}
		pg.NoIndex = true
	}
	if v, ok := m.Peek("params"); ok {
		pm, ok := v.(*yamlite.Map)
		if !ok {
			return nil, fmt.Errorf("%s: params must be a mapping", r.file)
		}
		pg.Params = pm
	} else {
		pg.Params = yamlite.NewMap()
	}
	slices.SortFunc(r.res, func(a, b resFile) int { return strings.Compare(a.name, b.name) })
	for _, rf := range r.res {
		name := rf.name
		res := &Resource{Name: name, page: pg, src: path.Join(path.Dir(r.file), name), RelPermalink: pg.RelPermalink + name, MediaType: mediaType(name), stamp: rf.stamp}
		pg.Resources = append(pg.Resources, res)
	}
	return pg, nil
}

func mediaType(name string) string {
	t := mime.TypeByExtension(path.Ext(name))
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	return t
}

func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func (p *Project) loadAssets() error {
	p.assets = map[string]*Resource{}
	dir := filepath.Join(p.Root, "assets")
	err := filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, full)
		rel = filepath.ToSlash(rel)
		ext := path.Ext(rel)
		if ext != ".css" && ext != ".js" {
			p.errs.add("assets/%s: only .css and .js files belong in assets/; put other files in static/ or a page bundle", rel)
			return nil
		}
		p.assets[rel] = &Resource{Name: rel, src: path.Join("assets", rel), asset: true, MediaType: mediaType(rel)}
		return nil
	})
	return err
}

func (p *Project) loadStatic() error {
	dir := filepath.Join(p.Root, "static")
	return filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(dir, full)
		rel = filepath.ToSlash(rel)
		switch rel {
		case "robots.txt", "sitemap.xml", "404.html":
			p.errs.add("static/%s: lyn generates this file; remove it from static/", rel)
		}
		p.static = append(p.static, rel)
		if fi, err := d.Info(); err == nil {
			p.staticStamps = append(p.staticStamps, fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano()))
		} else {
			p.staticStamps = append(p.staticStamps, "")
		}
		return nil
	})
}

func (p *Project) buildMenus() error {
	for _, s := range p.Sites {
		menus := yamlite.NewMap()
		for i, name := range s.Menus.Keys {
			list, ok := s.Menus.Vals[i].([]any)
			if !ok {
				return fmt.Errorf("site.yml: menu %q must be a list", name)
			}
			byName := map[string]*MenuEntry{}
			var roots []*MenuEntry
			var all []*MenuEntry
			for _, item := range list {
				m, ok := item.(*yamlite.Map)
				if !ok {
					return fmt.Errorf("site.yml: menu %q entries must be mappings", name)
				}
				if err := expectKeys(m, "site.yml", "name", "pageRef", "url", "weight", "parent", "params", "title"); err != nil {
					return err
				}
				e := &MenuEntry{Params: yamlite.NewMap()}
				var err error
				if e.Name, err = str(m, "name", "site.yml", true); err != nil {
					return err
				}
				e.Title, _ = str(m, "title", "site.yml", false)
				e.parent, _ = str(m, "parent", "site.yml", false)
				if v, ok := m.Peek("weight"); ok {
					e.Weight, _ = v.(int)
				}
				if v, ok := m.Peek("params"); ok {
					if pm, ok := v.(*yamlite.Map); ok {
						e.Params = pm
						p.trackMap(pm, "menus."+name+"."+e.Name+".params")
					}
				}
				ref, _ := str(m, "pageRef", "site.yml", false)
				u, _ := str(m, "url", "site.yml", false)
				switch {
				case ref != "" && u != "":
					return fmt.Errorf("site.yml: menu entry %q has both pageRef and url", e.Name)
				case ref != "":
					key := "/" + strings.Trim(ref, "/") + "/"
					if key == "//" {
						key = "/"
					}
					pg := s.byPath[key]
					if pg == nil {
						p.errs.add("site.yml: menu %q entry %q points to %s, which is not a %s page", name, e.Name, ref, s.Language.Code)
						continue
					}
					e.Page, e.URL = pg, pg.RelPermalink
				case u != "":
					e.URL = u
				}
				if _, dup := byName[e.Name]; dup {
					return fmt.Errorf("site.yml: menu %q has two entries named %q", name, e.Name)
				}
				byName[e.Name] = e
				all = append(all, e)
			}
			for _, e := range all {
				if e.parent == "" {
					roots = append(roots, e)
					continue
				}
				parent, ok := byName[e.parent]
				if !ok {
					return fmt.Errorf("site.yml: menu entry %q has unknown parent %q", e.Name, e.parent)
				}
				parent.Children = append(parent.Children, e)
			}
			sortEntries := func(list []any) {
				slices.SortStableFunc(list, func(a, b any) int {
					return cmp.Compare(a.(*MenuEntry).Weight, b.(*MenuEntry).Weight)
				})
			}
			var out []any
			for _, e := range roots {
				out = append(out, e)
			}
			sortEntries(out)
			for _, e := range all {
				sortEntries(e.Children)
			}
			menus.Set(name, out, 0)
		}
		s.Menus = menus
	}
	return p.errs.err()
}

func walkParallel(root string, fn func(rel string, d fs.DirEntry) error) error {
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 12)
	var visit func(rel string)
	visit = func(rel string) {
		defer wg.Done()
		sem <- struct{}{}
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		<-sem
		if err != nil {
			errMu.Lock()
			firstErr = cmp.Or(firstErr, err)
			errMu.Unlock()
			return
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			child := e.Name()
			if rel != "" {
				child = rel + "/" + child
			}
			sem <- struct{}{}
			err := fn(child, e)
			<-sem
			if err != nil {
				errMu.Lock()
				firstErr = cmp.Or(firstErr, err)
				errMu.Unlock()
				return
			}
			if e.IsDir() {
				wg.Add(1)
				go visit(child)
			}
		}
	}
	wg.Add(1)
	visit("")
	wg.Wait()
	return firstErr
}
