package build

import (
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/beint-no/lyn/yamlite"
)

type resList []*Resource

type accessor struct {
	name    string
	minArgs int
	maxArgs int
	page    func(s *st, p *Page, args []any, loc string) any
	site    func(s *st, x *Site, args []any, loc string) any
	lang    func(x *Language) any
	menu    func(x *MenuEntry) any
	res     func(s *st, x *Resource) any
	resList func(s *st, x resList, args []any, loc string) any
	time    func(x time.Time, args []any, loc string) any
	sc      func(s *st, x *shortcodeCtx, args []any, loc string) any
	key     string
}

var accessors = map[string]*accessor{}
var keyAccessors = map[string]*accessor{}
var keyAccessorsMu sync.Mutex

func acc(name string) *accessor {
	a := accessors[name]
	if a == nil {
		a = &accessor{name: name}
		accessors[name] = a
	}
	return a
}

func lookupAccessor(name string) *accessor {
	if name == "" {
		return nil
	}
	if unicode.IsUpper(rune(name[0])) {
		return accessors[name]
	}
	keyAccessorsMu.Lock()
	defer keyAccessorsMu.Unlock()
	if a, ok := keyAccessors[name]; ok {
		return a
	}
	a := &accessor{name: name, key: name}
	keyAccessors[name] = a
	return a
}

func (a *accessor) get(s *st, v any, args []any, loc string) any {
	if a.key != "" {
		switch t := v.(type) {
		case *yamlite.Map:
			i := t.Find(a.key)
			if i < 0 {
				s.r.missMap, s.r.missRes, s.r.missKey = t, nil, a.key
				return nil
			}
			s.r.read(t, i)
			return t.Vals[i]
		case nil:
			fail(loc, "cannot read .%s: the value is missing%s", a.key, s.r.missHint())
		}
		fail(loc, "cannot read key .%s of %s (only maps have lowercase keys)", a.key, typeName(v))
	}
	switch t := v.(type) {
	case *Page:
		if a.page != nil {
			return a.page(s, t, args, loc)
		}
	case *Site:
		if a.site != nil {
			return a.site(s, t, args, loc)
		}
	case *Language:
		if a.lang != nil {
			return a.lang(t)
		}
	case *MenuEntry:
		if a.menu != nil {
			return a.menu(t)
		}
	case *Resource:
		if a.res != nil {
			s.r.useResource(t)
			return a.res(s, t)
		}
	case resList:
		if a.resList != nil {
			return a.resList(s, t, args, loc)
		}
	case time.Time:
		if a.time != nil {
			return a.time(t, args, loc)
		}
	case *shortcodeCtx:
		if a.sc != nil {
			return a.sc(s, t, args, loc)
		}
	case nil:
		fail(loc, "cannot read .%s: the value is missing%s", a.name, s.r.missHint())
	}
	fail(loc, "%s has no field .%s", typeName(v), a.name)
	return nil
}

func pageField(name string, fn func(s *st, p *Page) any) {
	acc(name).page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.meta(p)
		return fn(s, p)
	}
}

func pageMethod(name string, n int, fn func(s *st, p *Page, args []any, loc string) any) {
	a := acc(name)
	a.minArgs, a.maxArgs = n, n
	a.page = fn
}

func init() {
	pageField("Title", func(_ *st, p *Page) any { return p.Title })
	pageField("SeoTitle", func(_ *st, p *Page) any {
		if p.SeoTitle != "" {
			return p.SeoTitle
		}
		return p.Title
	})
	pageField("Description", func(_ *st, p *Page) any { return p.Description })
	acc("Summary").page = func(s *st, p *Page, _ []any, loc string) any {
		s.r.rec.meta(p)
		return s.r.summary(p, loc)
	}
	pageField("RelPermalink", func(_ *st, p *Page) any { return p.RelPermalink })
	pageField("Permalink", func(_ *st, p *Page) any { return p.site.BaseURL + p.RelPermalink })
	pageField("Kind", func(_ *st, p *Page) any { return p.Kind })
	pageField("IsHome", func(_ *st, p *Page) any { return p.Kind == "home" })
	pageField("IsSection", func(_ *st, p *Page) any { return p.Kind == "section" || p.Kind == "home" })
	pageField("IsPage", func(_ *st, p *Page) any { return p.Kind == "page" })
	pageField("Layout", func(_ *st, p *Page) any { return p.Layout })
	pageField("Weight", func(_ *st, p *Page) any { return p.Weight })
	pageField("Date", func(s *st, p *Page) any {
		if p.Kind == "section" || p.Kind == "home" {
			s.r.rec.rcoll(p)
		}
		return p.Date
	})
	pageField("Lastmod", func(s *st, p *Page) any {
		if p.Kind == "section" || p.Kind == "home" {
			s.r.rec.rcoll(p)
		}
		return p.Lastmod
	})
	pageField("NoIndex", func(_ *st, p *Page) any { return p.NoIndex })
	pageField("Params", func(_ *st, p *Page) any { return p.Params })
	pageField("Path", func(_ *st, p *Page) any { return p.Path })
	pageField("Site", func(_ *st, p *Page) any { return p.site })
	pageField("Language", func(_ *st, p *Page) any { return p.Lang })
	pageField("File", func(_ *st, p *Page) any { return p.File })
	pageField("Name", func(_ *st, p *Page) any { return path.Base(p.Path) })
	pageField("Section", func(_ *st, p *Page) any {
		t := strings.Trim(p.Path, "/")
		if i := strings.IndexByte(t, '/'); i >= 0 {
			return t[:i]
		}
		if p.Kind == "page" || p.Kind == "home" {
			if p.Parent == nil || p.Parent.Kind == "home" {
				return ""
			}
		}
		return t
	})
	pageField("Parent", func(s *st, p *Page) any {
		if p.Parent == nil {
			return nil
		}
		return p.Parent
	})
	pageField("Ancestors", func(s *st, p *Page) any {
		var out Pages
		for a := p.Parent; a != nil; a = a.Parent {
			out = append(out, a)
		}
		return out
	})
	pageField("CurrentSection", func(_ *st, p *Page) any {
		if p.Kind == "page" || p.Kind == "404" {
			return p.Parent
		}
		return p
	})
	acc("RawContent").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.body(p)
		return string(p.body)
	}
	acc("Pages").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.coll(p)
		return p.Children
	}
	acc("RegularPages").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.coll(p)
		var out Pages
		for _, c := range p.Children {
			if c.Kind == "page" {
				out = append(out, c)
			}
		}
		return out
	}
	acc("Sections").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.coll(p)
		var out Pages
		for _, c := range p.Children {
			if c.Kind == "section" {
				out = append(out, c)
			}
		}
		return out
	}
	acc("RegularPagesRecursive").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.rcoll(p)
		p.rprOnce.Do(func() {
			var walk func(*Page)
			walk = func(x *Page) {
				for _, c := range x.Children {
					if c.Kind == "page" {
						p.rpr = append(p.rpr, c)
					} else {
						walk(c)
					}
				}
			}
			walk(p)
			sortPages(p.rpr)
		})
		return p.rpr
	}
	acc("Translations").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.path("*" + p.group)
		return p.Translations
	}
	acc("AllTranslations").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.path("*" + p.group)
		out := make(Pages, 0, len(p.Translations)+1)
		for _, l := range p.site.project.Languages {
			if l == p.Lang {
				out = append(out, p)
				continue
			}
			for _, t := range p.Translations {
				if t.Lang == l {
					out = append(out, t)
				}
			}
		}
		return out
	}
	acc("IsTranslated").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.path("*" + p.group)
		return len(p.Translations) > 0
	}
	acc("Resources").page = func(s *st, p *Page, _ []any, _ string) any {
		s.r.rec.body(p)
		return resList(p.Resources)
	}
	acc("Card").page = func(s *st, p *Page, _ []any, loc string) any {
		if p.Kind == "404" {
			fail(loc, "the 404 page has no card")
		}
		s.r.rec.meta(p)
		return p.card
	}
	acc("Content").page = func(s *st, p *Page, _ []any, loc string) any {
		s.r.rec.body(p)
		return s.r.content(p, loc)
	}
	acc("TableOfContents").page = func(s *st, p *Page, _ []any, loc string) any {
		s.r.rec.body(p)
		s.r.content(p, loc)
		return p.toc
	}
	acc("WordCount").page = func(s *st, p *Page, _ []any, loc string) any {
		s.r.rec.body(p)
		s.r.content(p, loc)
		return p.wordCount()
	}
	acc("ReadingTime").page = func(s *st, p *Page, _ []any, loc string) any {
		s.r.rec.body(p)
		s.r.content(p, loc)
		return (p.wordCount() + 212) / 213
	}
	pageMethod("HasFragment", 1, func(s *st, p *Page, args []any, loc string) any {
		s.r.rec.body(p)
		s.r.content(p, loc)
		id := toString(args[0], loc)
		for _, x := range p.ids {
			if x == id {
				return true
			}
		}
		return false
	})
	pageMethod("GetPage", 1, func(s *st, p *Page, args []any, loc string) any {
		return s.r.getPage(p.site, p, toString(args[0], loc), loc)
	})
	pageMethod("Param", 1, func(s *st, p *Page, args []any, loc string) any {
		s.r.rec.meta(p)
		k := toString(args[0], loc)
		if i := p.Params.Find(k); i >= 0 {
			s.r.read(p.Params, i)
			return p.Params.Vals[i]
		}
		if i := p.site.Params.Find(k); i >= 0 {
			s.r.read(p.site.Params, i)
			return p.site.Params.Vals[i]
		}
		return nil
	})
	pageMethod("IsAncestor", 1, func(s *st, p *Page, args []any, loc string) any {
		o, ok := args[0].(*Page)
		if !ok {
			fail(loc, "IsAncestor needs a page")
		}
		return o != p && o.Lang == p.Lang && strings.HasPrefix(o.Path, p.Path)
	})
	pageMethod("IsDescendant", 1, func(s *st, p *Page, args []any, loc string) any {
		o, ok := args[0].(*Page)
		if !ok {
			fail(loc, "IsDescendant needs a page")
		}
		return o != p && o.Lang == p.Lang && strings.HasPrefix(p.Path, o.Path)
	})

	site := func(name string, fn func(s *st, x *Site) any) {
		acc(name).site = func(s *st, x *Site, _ []any, _ string) any { return fn(s, x) }
	}
	site("Title", func(_ *st, x *Site) any { return x.Title })
	site("BaseURL", func(_ *st, x *Site) any { return x.BaseURL + "/" })
	site("Params", func(_ *st, x *Site) any { return x.Params })
	site("Menus", func(_ *st, x *Site) any { return x.Menus })
	site("Home", func(_ *st, x *Site) any { return x.Home })
	site("Language", func(_ *st, x *Site) any { return x.Language })
	site("Data", func(s *st, x *Site) any { return s.r.p.data })
	site("Languages", func(_ *st, x *Site) any {
		out := make([]any, len(x.project.Languages))
		for i, l := range x.project.Languages {
			out[i] = l
		}
		return out
	})
	site("Pages", func(s *st, x *Site) any {
		s.r.rec.rcoll(x.Home)
		return x.Pages
	})
	site("RegularPages", func(s *st, x *Site) any {
		s.r.rec.rcoll(x.Home)
		x.regOnce.Do(func() {
			for _, p := range x.Pages {
				if p.Kind == "page" {
					x.regular = append(x.regular, p)
				}
			}
		})
		return x.regular
	})
	site("Sections", func(s *st, x *Site) any {
		s.r.rec.coll(x.Home)
		var out Pages
		for _, c := range x.Home.Children {
			if c.Kind == "section" {
				out = append(out, c)
			}
		}
		return out
	})
	g := acc("GetPage")
	g.site = func(s *st, x *Site, args []any, loc string) any {
		return s.r.getPage(x, nil, toString(args[0], loc), loc)
	}

	lang := func(name string, fn func(x *Language) any) { acc(name).lang = fn }
	lang("Code", func(x *Language) any { return x.Code })
	lang("Locale", func(x *Language) any { return x.Locale })
	lang("Label", func(x *Language) any { return x.Label })
	lang("Title", func(x *Language) any { return x.Title })
	lang("Weight", func(x *Language) any { return x.Weight })
	lang("Home", func(x *Language) any { return x.site.Home })

	menu := func(name string, fn func(x *MenuEntry) any) { acc(name).menu = fn }
	menu("Name", func(x *MenuEntry) any { return x.Name })
	menu("Title", func(x *MenuEntry) any {
		if x.Title != "" {
			return x.Title
		}
		return x.Name
	})
	menu("URL", func(x *MenuEntry) any { return x.URL })
	menu("Page", func(x *MenuEntry) any {
		if x.Page == nil {
			return nil
		}
		return x.Page
	})
	menu("Weight", func(x *MenuEntry) any { return x.Weight })
	menu("Params", func(x *MenuEntry) any { return x.Params })
	menu("Children", func(x *MenuEntry) any { return x.Children })
	menu("HasChildren", func(x *MenuEntry) any { return len(x.Children) > 0 })

	res := func(name string, fn func(s *st, x *Resource) any) { acc(name).res = fn }
	res("Name", func(_ *st, x *Resource) any { return x.Name })
	res("RelPermalink", func(s *st, x *Resource) any { return s.r.resourceURL(x) })
	res("Permalink", func(s *st, x *Resource) any { return s.r.p.BaseURL + s.r.resourceURL(x) })
	res("Integrity", func(s *st, x *Resource) any { s.r.resourceURL(x); return x.Integrity })
	res("MediaType", func(_ *st, x *Resource) any { return x.MediaType })
	res("Content", func(s *st, x *Resource) any {
		b := s.r.resourceData(x)
		if x.MediaType == "image/svg+xml" {
			return HTML(b)
		}
		return string(b)
	})
	res("Width", func(s *st, x *Resource) any { w, _ := s.r.imageSize(x); return w })
	res("Height", func(s *st, x *Resource) any { _, h := s.r.imageSize(x); return h })

	rl := acc("Get")
	rl.minArgs, rl.maxArgs = 1, 1
	rl.resList = func(s *st, x resList, args []any, loc string) any {
		name := toString(args[0], loc)
		for _, r := range x {
			if r.Name == name {
				s.r.useResource(r)
				return r
			}
		}
		s.r.missMap, s.r.missRes, s.r.missKey = nil, x, name
		return nil
	}
	rl.sc = func(s *st, x *shortcodeCtx, args []any, loc string) any {
		switch k := args[0].(type) {
		case int:
			if k < len(x.positional) {
				return x.positional[k]
			}
			return nil
		default:
			v, _ := x.named.Get(toString(k, loc))
			return v
		}
	}
	mt := acc("Match")
	mt.minArgs, mt.maxArgs = 1, 1
	mt.resList = func(s *st, x resList, args []any, loc string) any {
		pattern := toString(args[0], loc)
		var out resList
		for _, r := range x {
			if ok, _ := path.Match(pattern, r.Name); ok {
				out = append(out, r)
			}
		}
		return out
	}

	tm := func(name string, n int, fn func(x time.Time, args []any, loc string) any) {
		a := acc(name)
		a.minArgs, a.maxArgs = n, n
		a.time = fn
	}
	tm("Format", 1, func(x time.Time, args []any, loc string) any { return x.Format(toString(args[0], loc)) })
	tm("Year", 0, func(x time.Time, _ []any, _ string) any { return x.Year() })
	tm("IsZero", 0, func(x time.Time, _ []any, _ string) any { return x.IsZero() })

	acc("Inner").sc = func(s *st, x *shortcodeCtx, _ []any, _ string) any { return x.inner }
	acc("Page").sc = func(s *st, x *shortcodeCtx, _ []any, _ string) any { return x.page }
	acc("Params").sc = func(s *st, x *shortcodeCtx, _ []any, _ string) any { return x.named }
}

type shortcodeCtx struct {
	page       *Page
	named      *yamlite.Map
	positional []any
	inner      HTML
}
