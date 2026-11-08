package build

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/beint-no/lyn/yamlite"
	"github.com/zeebo/xxh3"
)

const stateVersion = 4

type PageState struct {
	Meta       uint64
	Body       uint64
	Sort       uint64
	File       string
	Shortcodes []string
}

type OutRecord struct {
	Page      string
	Hash      uint64
	Templates []string
	Metas     []string
	Bodies    []string
	Colls     []string
	RColls    []string
	Paths     []string
	I18n      []string
	Reads     []string
	Res       []string
	Year      bool
	Scan      Scan
}

type State struct {
	Version   int
	Binary    string
	Config    uint64
	Year      int
	Templates map[string]uint64
	I18n      map[string]uint64
	Data      map[string]uint64
	Assets    map[string]uint64
	Statics   map[string]uint64
	Pages     map[string]PageState
	Outputs   map[string]*OutRecord
	Files     map[string]string
	OutID     string
}

type Result struct {
	Pages    int
	Rendered int
	Reused   int
	Written  int
	Copied   int
	Removed  int
	Full     bool
	Duration time.Duration
	Phases   []Phase
}

type Phase struct {
	Name string
	Dur  time.Duration
}

func (r *Result) String() string {
	mode := "incremental"
	if r.Full {
		mode = "full"
	}
	var ph []string
	for _, p := range r.Phases {
		ph = append(ph, fmt.Sprintf("%s %s", p.Name, p.Dur.Round(time.Microsecond*100)))
	}
	return fmt.Sprintf("%d pages (%s): %d rendered, %d reused, %d written, %d copied, %d removed in %s [%s]",
		r.Pages, mode, r.Rendered, r.Reused, r.Written, r.Copied, r.Removed, r.Duration.Round(time.Millisecond/10), strings.Join(ph, ", "))
}

type Builder struct {
	Root     string
	cache    ContentCache
	hints    map[string]bool
	mu       sync.Mutex
	state    *State
	loaded   bool
	Persist  bool
	LastErrs error
}

func NewBuilder(root string) *Builder {
	return &Builder{Root: root, Persist: true}
}

func (b *Builder) Changed(path string, structural bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if structural {
		b.cache = ContentCache{}
		return
	}
	if b.hints == nil {
		b.hints = map[string]bool{}
	}
	b.hints[path] = true
}

func (b *Builder) statePath() string { return filepath.Join(b.Root, ".lyn", "state.gob") }

func binaryID() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return "unknown"
	}
	id := fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
	if bi, ok := debug.ReadBuildInfo(); ok {
		id += bi.Main.Version
	}
	return id
}

func (b *Builder) loadState() {
	if b.loaded {
		return
	}
	b.loaded = true
	if !b.Persist {
		return
	}
	f, err := os.Open(b.statePath())
	if err != nil {
		return
	}
	defer f.Close()
	var s State
	if err := gob.NewDecoder(f).Decode(&s); err != nil {
		return
	}
	if s.Version != stateVersion || s.Binary != binaryID() {
		return
	}
	b.state = &s
}

func (b *Builder) saveState(s *State) error {
	b.state = s
	if !b.Persist {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(b.statePath()), 0o755); err != nil {
		return err
	}
	tmp := b.statePath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, b.statePath())
}

func pageKey(pg *Page) string {
	if pg.Kind == "404" {
		return "404:" + pg.Lang.Code
	}
	return pg.Lang.Code + ":" + pg.Path
}

func (b *Builder) Build() (*Result, error) {
	start := time.Now()
	res := &Result{}
	phase := func(name string, t time.Time) time.Time {
		now := time.Now()
		res.Phases = append(res.Phases, Phase{name, now.Sub(t)})
		return now
	}
	t := time.Now()
	b.loadState()
	t = phase("state", t)
	b.mu.Lock()
	hints := b.hints
	b.hints = nil
	b.mu.Unlock()
	p, err := LoadWith(b.Root, &b.cache, hints)
	if err != nil {
		b.LastErrs = err
		return nil, err
	}
	t = phase("load", t)
	p.prepare()
	old := b.state
	if old != nil && old.OutID != dirID(p.Out) {
		old = nil
	}
	full := old == nil || old.Config != p.configHash
	res.Full = full
	var outs []*output
	byOut := map[string]*output{}
	for _, s := range p.Sites {
		for _, pg := range append(slices.Clone(s.Pages), s.notFound) {
			l, err := p.layoutFor(pg)
			if err != nil {
				if pg.Kind == "404" {
					if _, ok := p.tpl.layouts["404"]; !ok {
						p.errs.add("layouts/404.html is required")
					}
					continue
				}
				p.errs.add("%v", err)
				continue
			}
			o := &output{page: pg, path: outPath(pg), layout: l}
			outs = append(outs, o)
			byOut[o.path] = o
		}
	}
	if err := p.errs.err(); err != nil {
		b.LastErrs = err
		return nil, err
	}
	for _, a := range sortedKeys(p.assets) {
		p.processAsset(p.assets[a])
		if err := p.assets[a].procErr; err != nil {
			p.errs.add("assets/%s: %v", a, err)
		}
	}
	ns := p.newState()
	dirty := b.dirty(p, old, ns, outs, full)
	t = phase("plan", t)

	_, statErr := os.Stat(p.Out)
	p.outExisted = statErr == nil
	files := map[string]bool{}
	for _, o := range outs {
		files[o.path] = true
	}
	copies := p.planCopies(old, ns, files)
	var dirs []string
	for _, o := range outs {
		if dirty[o.path] {
			dirs = append(dirs, path.Dir(o.path))
		}
	}
	for _, c := range copies {
		dirs = append(dirs, path.Dir(c.dst))
	}
	p.makeDirs(dirs)
	wr := p.startWriters()
	go func() {
		for _, c := range copies {
			wr.ch <- c
		}
		wr.producers.Done()
	}()
	records := make(map[string]*OutRecord, len(outs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan *output)
	written := 0
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for o := range work {
				html, rec, err := p.render(o)
				if err != nil {
					p.errs.add("%v", err)
					continue
				}
				r := p.toRecord(o, rec, html)
				var prev *OutRecord
				if old != nil {
					prev = old.Outputs[o.path]
				}
				if prev == nil || prev.Hash != r.Hash {
					wr.ch <- writeJob{dst: o.path, data: html}
					mu.Lock()
					written++
					mu.Unlock()
				}
				mu.Lock()
				records[o.path] = r
				mu.Unlock()
			}
		})
	}
	for _, o := range outs {
		if dirty[o.path] {
			work <- o
		}
	}
	close(work)
	wg.Wait()
	wr.producers.Done()
	res.Rendered = len(records)
	for _, o := range outs {
		if !dirty[o.path] {
			records[o.path] = old.Outputs[o.path]
			res.Reused++
		}
	}
	res.Written = written
	res.Pages = len(outs)
	for _, pg := range p.pages {
		k := pageKey(pg)
		st := ns.Pages[k]
		if pg.contentRec != nil {
			st.Shortcodes = pg.contentRec.shortcodes
		} else if old != nil {
			st.Shortcodes = old.Pages[k].Shortcodes
		}
		ns.Pages[k] = st
	}
	ns.Outputs = records
	wr.wait()
	res.Copied = len(copies)
	t = phase("render+write", t)

	sitemap, robots := p.sitemap(records), p.robots()
	for name, data := range map[string][]byte{"sitemap.xml": sitemap, "robots.txt": robots} {
		files[name] = true
		target := filepath.Join(p.Out, name)
		if cur, err := os.ReadFile(target); err != nil || !bytes.Equal(cur, data) {
			if err := writeFile(target, data); err != nil {
				p.errs.add("%v", err)
			}
		}
	}
	res.Removed = p.removeStale(old, files)
	t = phase("write", t)
	p.check(ns, files)
	t = phase("check", t)
	ns.OutID = dirID(p.Out)
	if err := b.saveState(ns); err != nil {
		p.errs.add("saving build state: %v", err)
	}
	phase("save", t)
	res.Duration = time.Since(start)
	b.LastErrs = p.errs.err()
	return res, b.LastErrs
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func dirID(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writeFile(target string, data []byte) error {
	if err := os.WriteFile(target, data, 0o644); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}
	return nil
}

func (p *Project) prepare() {
	for _, a := range sortedKeys(p.assets) {
		res := p.assets[a]
		res.idx = int32(len(p.resources))
		p.resources = append(p.resources, res)
	}
	for _, pg := range p.pages {
		for _, r := range pg.Resources {
			r.idx = int32(len(p.resources))
			p.resources = append(p.resources, r)
		}
		h := xxh3.New()
		h.WriteString(pg.Title)
		h.WriteString(strconv.Itoa(pg.Weight))
		h.WriteString(pg.Date.String())
		pg.sortHash = h.Sum64()
		p.trackMap(pg.Params, pg.File+"#params")
	}
}

func resKey(r *Resource) string {
	if r.asset {
		return "asset:" + r.Name
	}
	return r.page.File + "#" + r.Name
}

func (p *Project) newState() *State {
	s := &State{Version: stateVersion, Binary: binaryID(), Config: p.configHash, Year: time.Now().Year(),
		Templates: map[string]uint64{}, I18n: map[string]uint64{}, Data: map[string]uint64{}, Assets: map[string]uint64{}, Statics: map[string]uint64{},
		Pages: map[string]PageState{}, Files: map[string]string{}}
	for i, f := range p.tpl.files {
		s.Templates[f] = p.tpl.fileHash[i]
	}
	for lang, m := range p.i18n {
		for k, v := range m {
			s.I18n[lang+":"+k] = xxh3.HashString(v)
		}
	}
	for i, k := range p.data.Keys {
		h := xxh3.New()
		gobHash(h, p.data.Vals[i])
		s.Data["data."+k] = h.Sum64()
	}
	for name, a := range p.assets {
		s.Assets["asset:"+name] = xxh3.Hash(a.data)
	}
	for _, f := range p.static {
		s.Statics[f] = 1
	}
	for _, pg := range p.pages {
		body := pg.bodyHash
		for _, r := range pg.Resources {
			body ^= xxh3.HashString(r.Name + ":" + r.stamp)
		}
		pg.bodyHash = body
		s.Pages[pageKey(pg)] = PageState{Meta: pg.metaHash, Body: body, Sort: pg.sortHash, File: pg.File}
	}
	return s
}

func gobHash(w io.Writer, v any) {
	switch t := v.(type) {
	case *yamlite.Map:
		for i, k := range t.Keys {
			fmt.Fprintf(w, "{%s:", k)
			gobHash(w, t.Vals[i])
			io.WriteString(w, "}")
		}
	case []any:
		for _, e := range t {
			io.WriteString(w, "[")
			gobHash(w, e)
			io.WriteString(w, "]")
		}
	default:
		fmt.Fprintf(w, "%T:%v", v, v)
	}
}

func (b *Builder) dirty(p *Project, old, ns *State, outs []*output, full bool) map[string]bool {
	d := make(map[string]bool, len(outs))
	if full {
		for _, o := range outs {
			d[o.path] = true
		}
		return d
	}
	changedTpl := map[string]bool{}
	for f, h := range ns.Templates {
		if old.Templates[f] != h {
			changedTpl[f] = true
		}
	}
	for f := range old.Templates {
		if _, ok := ns.Templates[f]; !ok {
			changedTpl[f] = true
		}
	}
	metaChanged, bodyChanged := map[string]bool{}, map[string]bool{}
	collChanged := map[string]bool{}
	rcollChanged := map[string]bool{}
	pathChanged := map[string]bool{}
	markStructure := func(key string) {
		lang, pth, _ := strings.Cut(key, ":")
		pathChanged[key] = true
		pathChanged["*"+pth] = true
		if pth != "/" {
			parent := lang + ":" + parentPath(pth)
			collChanged[parent] = true
		}
		for a := pth; ; a = parentPath(a) {
			rcollChanged[lang+":"+a] = true
			if a == "/" {
				break
			}
		}
	}
	for k, ps := range ns.Pages {
		op, ok := old.Pages[k]
		if !ok {
			metaChanged[k], bodyChanged[k] = true, true
			markStructure(k)
			continue
		}
		if op.Meta != ps.Meta {
			metaChanged[k] = true
			lang, pth, _ := strings.Cut(k, ":")
			if pth != "/" && lang != "404" {
				collChanged[lang+":"+parentPath(pth)] = true
				for a := parentPath(pth); ; a = parentPath(a) {
					rcollChanged[lang+":"+a] = true
					if a == "/" {
						break
					}
				}
			}
		}
		if op.Body != ps.Body {
			bodyChanged[k] = true
		}
		for _, sc := range op.Shortcodes {
			if changedTpl["layouts/shortcodes/"+sc+".html"] {
				bodyChanged[k] = true
			}
		}
		if op.Sort != ps.Sort {
			lang, pth, _ := strings.Cut(k, ":")
			if pth != "/" {
				collChanged[lang+":"+parentPath(pth)] = true
			}
			for a := pth; ; a = parentPath(a) {
				rcollChanged[lang+":"+a] = true
				if a == "/" {
					break
				}
			}
		}
	}
	for k := range old.Pages {
		if _, ok := ns.Pages[k]; !ok {
			metaChanged[k], bodyChanged[k] = true, true
			markStructure(k)
		}
	}
	changedKeys := func(a, b map[string]uint64) map[string]bool {
		out := map[string]bool{}
		for k, v := range a {
			if b[k] != v {
				out[k] = true
			}
		}
		for k := range b {
			if _, ok := a[k]; !ok {
				out[k] = true
			}
		}
		return out
	}
	i18nChanged := changedKeys(ns.I18n, old.I18n)
	dataChanged := changedKeys(ns.Data, old.Data)
	assetChanged := changedKeys(ns.Assets, old.Assets)
	staticChanged := len(changedKeys(ns.Statics, old.Statics)) > 0
	yearChanged := ns.Year != old.Year
	any := func(list []string, set map[string]bool) bool {
		if len(set) == 0 {
			return false
		}
		for _, x := range list {
			if set[x] {
				return true
			}
		}
		return false
	}
	langs := make([]string, 0, len(p.Languages))
	for _, l := range p.Languages {
		langs = append(langs, l.Code)
	}
	for _, o := range outs {
		r := old.Outputs[o.path]
		switch {
		case r == nil, r.Page != pageKey(o.page), staticChanged,
			any(r.Templates, changedTpl),
			any(r.Metas, metaChanged),
			any(r.Bodies, bodyChanged),
			any(r.Colls, collChanged),
			any(r.RColls, rcollChanged),
			any(r.Paths, pathChanged),
			any(r.Res, assetChanged),
			r.Year && yearChanged:
			d[o.path] = true
			continue
		}
		if len(i18nChanged) > 0 {
			for _, k := range r.I18n {
				for _, l := range langs {
					if i18nChanged[l+":"+k] {
						d[o.path] = true
					}
				}
			}
		}
		if len(dataChanged) > 0 {
			for _, k := range r.Reads {
				for dk := range dataChanged {
					if strings.HasPrefix(k, dk+".") || strings.HasPrefix(k, dk+"\x00") || strings.HasPrefix(k, dk+"[") {
						d[o.path] = true
					}
				}
			}
		}
	}
	return d
}

func (p *Project) toRecord(o *output, rec *recorder, html []byte) *OutRecord {
	r := &OutRecord{Page: pageKey(o.page), Hash: xxh3.Hash(html), Year: rec.year}
	for _, id := range o.layout.files {
		r.Templates = append(r.Templates, p.tpl.files[id])
	}
	keys := func(list []int32) []string {
		out := make([]string, 0, len(list))
		for _, i := range list {
			out = append(out, pageKey(p.pages[i]))
		}
		return out
	}
	covered := map[*Page]bool{}
	for _, i := range rec.colls {
		covered[p.pages[i]] = true
	}
	rcov := map[*Page]bool{}
	for _, i := range rec.rcolls {
		rcov[p.pages[i]] = true
	}
	metas := rec.metas[:0:0]
	for _, i := range rec.metas {
		pg := p.pages[i]
		if pg.Parent != nil && covered[pg.Parent] {
			continue
		}
		skip := false
		for a := pg.Parent; a != nil && len(rcov) > 0; a = a.Parent {
			if rcov[a] {
				skip = true
				break
			}
		}
		if !skip {
			metas = append(metas, i)
		}
	}
	r.Metas, r.Bodies, r.Colls, r.RColls = keys(metas), keys(rec.bodies), keys(rec.colls), keys(rec.rcolls)
	r.Paths = rec.paths
	for _, i := range rec.i18n {
		r.I18n = append(r.I18n, p.i18nKeys[i])
	}
	for _, x := range rec.reads {
		m := p.maps[x>>32]
		r.Reads = append(r.Reads, m.Path+"\x00"+m.Keys[uint32(x)])
	}
	for _, i := range rec.res {
		r.Res = append(r.Res, resKey(p.resources[i]))
	}
	r.Metas = compactSorted(r.Metas)
	r.Scan = scanHTML(html)
	return r
}

type writeJob struct {
	dst  string
	src  string
	data []byte
	res  *Resource
}

type writers struct {
	ch        chan writeJob
	producers sync.WaitGroup
	done      sync.WaitGroup
	p         *Project
}

func (p *Project) startWriters() *writers {
	w := &writers{ch: make(chan writeJob, 256), p: p}
	w.producers.Add(2)
	for range ioWorkers {
		w.done.Go(func() {
			for j := range w.ch {
				data := j.data
				if j.res != nil {
					b, err := p.resourceBytes(j.res)
					if err != nil {
						p.errs.add("%v", err)
						continue
					}
					data = b
				} else if j.src != "" {
					b, err := os.ReadFile(j.src)
					if err != nil {
						p.errs.add("%v", err)
						continue
					}
					data = b
				}
				if err := writeFile(filepath.Join(p.Out, filepath.FromSlash(j.dst)), data); err != nil {
					p.errs.add("%v", err)
				}
			}
		})
	}
	go func() {
		w.producers.Wait()
		close(w.ch)
	}()
	return w
}

func (w *writers) wait() { w.done.Wait() }

func (p *Project) planCopies(old, ns *State, files map[string]bool) []writeJob {
	type job struct {
		src, dst, stamp string
		res             *Resource
	}
	var jobs []job
	for i, f := range p.static {
		jobs = append(jobs, job{filepath.Join(p.Root, "static", f), f, p.staticStamps[i], nil})
	}
	for _, pg := range p.pages {
		for _, r := range pg.Resources {
			jobs = append(jobs, job{"", strings.TrimPrefix(r.RelPermalink, "/"), r.stamp, r})
		}
	}
	var todo []writeJob
	for _, j := range jobs {
		if files[j.dst] {
			p.errs.add("%s is produced twice (a static file or resource collides with a page)", j.dst)
			continue
		}
		files[j.dst] = true
		ns.Files[j.dst] = j.stamp
		if old != nil && old.Files[j.dst] == j.stamp && j.stamp != "" {
			continue
		}
		todo = append(todo, writeJob{src: j.src, dst: j.dst, res: j.res})
	}
	for _, name := range sortedKeys(p.assets) {
		a := p.assets[name]
		if a.data == nil {
			continue
		}
		files[a.outName] = true
		ns.Files[a.outName] = "asset"
		if old != nil && old.Assets["asset:"+a.Name] == xxh3.Hash(a.data) && old.Files[a.outName] == "asset" {
			continue
		}
		todo = append(todo, writeJob{dst: a.outName, data: a.data})
	}
	return todo
}

const ioWorkers = 6

func (p *Project) makeDirs(dirs []string) {
	seen := map[string]bool{}
	var all []string
	for _, d := range dirs {
		for d != "." && d != "" && d != "/" && !seen[d] {
			seen[d] = true
			all = append(all, d)
			d = path.Dir(d)
		}
	}
	slices.SortFunc(all, func(a, b string) int {
		return cmpInt(strings.Count(a, "/"), strings.Count(b, "/"), a, b)
	})
	os.MkdirAll(p.Out, 0o755)
	for len(all) > 0 {
		depth := strings.Count(all[0], "/")
		n := 0
		for n < len(all) && strings.Count(all[n], "/") == depth {
			n++
		}
		level := all[:n]
		all = all[n:]
		var wg sync.WaitGroup
		ch := make(chan string)
		for range ioWorkers {
			wg.Go(func() {
				for d := range ch {
					os.Mkdir(filepath.Join(p.Out, filepath.FromSlash(d)), 0o755)
				}
			})
		}
		for _, d := range level {
			ch <- d
		}
		close(ch)
		wg.Wait()
	}
}

func cmpInt(a, b int, sa, sb string) int {
	if a != b {
		return a - b
	}
	return strings.Compare(sa, sb)
}

func (p *Project) removeStale(old *State, files map[string]bool) int {
	removed := 0
	if old != nil {
		var prev []string
		for k := range old.Outputs {
			prev = append(prev, k)
		}
		for k := range old.Files {
			prev = append(prev, k)
		}
		for _, k := range prev {
			if !files[k] {
				if os.Remove(filepath.Join(p.Out, filepath.FromSlash(k))) == nil {
					removed++
				}
			}
		}
	}
	if old == nil && !p.outExisted {
		return removed
	}
	if old != nil {
		if removed > 0 {
			removeEmptyDirs(p.Out)
		}
		return removed
	}
	filepath.WalkDir(p.Out, func(full string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(p.Out, full)
		if !files[filepath.ToSlash(rel)] {
			if os.Remove(full) == nil {
				removed++
			}
		}
		return nil
	})
	if removed > 0 {
		removeEmptyDirs(p.Out)
	}
	return removed
}

func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && full != root {
			dirs = append(dirs, full)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}
}

func (p *Project) sitemap(records map[string]*OutRecord) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")
	for _, s := range p.Sites {
		for _, pg := range s.Pages {
			r := records[outPath(pg)]
			if pg.NoIndex || r == nil || r.Scan.NoIndex {
				continue
			}
			b.WriteString("<url><loc>")
			b.Write(escapeHTML(nil, p.BaseURL+pg.RelPermalink))
			b.WriteString("</loc>")
			if !pg.Lastmod.IsZero() {
				b.WriteString("<lastmod>" + pg.Lastmod.Format("2006-01-02") + "</lastmod>")
			}
			if len(pg.Translations) > 0 {
				for _, l := range p.Languages {
					for _, t := range append(Pages{pg}, pg.Translations...) {
						if t.Lang == l && !t.NoIndex {
							fmt.Fprintf(&b, `<xhtml:link rel="alternate" hreflang="%s" href="%s"/>`, l.Locale, escapeHTML(nil, p.BaseURL+t.RelPermalink))
						}
					}
				}
			}
			b.WriteString("</url>\n")
		}
	}
	b.WriteString("</urlset>\n")
	return b.Bytes()
}

func (p *Project) robots() []byte {
	return []byte("User-agent: *\nAllow: /\n\nSitemap: " + p.BaseURL + "/sitemap.xml\n")
}
