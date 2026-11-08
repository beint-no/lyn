package build

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
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

const stateVersion = 8

type PageState struct {
	Meta       uint64
	Body       uint64
	Sort       uint64
	File       string
	Group      string
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
	Cards     []string
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
	AssetOut  map[string]string
	SVGs      map[string]string
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
	scan     *scanCache
	stateLen int
	baseLen  int
	stamp    string
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

func (b *Builder) statePath() string { return filepath.Join(b.Root, ".lyn", "state") }

func (b *Builder) scanPath() string { return filepath.Join(b.Root, ".lyn", "content") }

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
	if raw, err := os.ReadFile(b.scanPath()); err == nil {
		b.scan, _ = decodeScan(b.Root, raw)
	}
	f, err := os.Open(b.statePath())
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	raw := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, raw); err != nil {
		return
	}
	s, base, err := decodeState(raw)
	if err != nil || s.Version != stateVersion || s.Binary != binaryID() {
		return
	}
	b.state, b.stateLen, b.baseLen, b.stamp = s, len(raw), base, scanStamp(fi)
}

func (b *Builder) saveState(s *State) error {
	prev := b.state
	b.state = s
	if !b.Persist || prev != nil && sameState(prev, s) && exists(b.statePath()) {
		return nil
	}
	if prev != nil && b.baseLen > 0 {
		var changed, removed []string
		for k, r := range s.Outputs {
			if prev.Outputs[k] != r {
				changed = append(changed, k)
			}
		}
		for k := range prev.Outputs {
			if _, ok := s.Outputs[k]; !ok {
				removed = append(removed, k)
			}
		}
		seg := encodeStateSegment(s, changed, removed)
		if b.stateLen+len(seg) <= 2*b.baseLen && b.appendState(seg) == nil {
			return nil
		}
	}
	b.baseLen = 0
	if err := os.MkdirAll(filepath.Dir(b.statePath()), 0o755); err != nil {
		return err
	}
	data := encodeState(s)
	tmp := b.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.statePath()); err != nil {
		return err
	}
	fi, err := os.Stat(b.statePath())
	if err != nil {
		return err
	}
	b.stateLen, b.baseLen, b.stamp = len(data), len(data), scanStamp(fi)
	return nil
}

func (b *Builder) appendState(seg []byte) error {
	f, err := os.OpenFile(b.statePath(), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || scanStamp(fi) != b.stamp {
		return errState
	}
	if _, err := f.Write(seg); err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	b.stateLen += len(seg)
	b.stamp = scanStamp(fi)
	return nil
}

func (b *Builder) saveScan(sv *scanSave) error {
	if sv == nil {
		return nil
	}
	b.scan = sv.cache
	if !b.Persist {
		return nil
	}
	if !sv.full {
		if f, err := os.OpenFile(b.scanPath(), os.O_WRONLY|os.O_APPEND, 0); err == nil {
			_, err := f.Write(sv.segment)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			return err
		}
		sv.segment = encodeSegment(sv.cache, keysOf(sv.cache.dirs), keysOf(sv.cache.files), nil, nil)
		sv.cache.size = len(scanMagic) + len(sv.segment)
	}
	if err := os.MkdirAll(filepath.Dir(b.scanPath()), 0o755); err != nil {
		return err
	}
	tmp := b.scanPath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = f.WriteString(scanMagic)
	if err == nil {
		_, err = f.Write(sv.segment)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, b.scanPath())
}

func pageKey(pg *Page) string { return pg.key }

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
	b.cache.prevScan, b.cache.scan = b.scan, nil
	p, err := LoadWith(b.Root, &b.cache, hints)
	ss := b.cache.scan
	if ss != nil && ss.partial {
		ss = nil
	}
	if err != nil {
		if ss != nil {
			b.saveScan(ss.save())
		}
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
	p.outputs = byOut
	if err := p.errs.err(); err != nil {
		b.LastErrs = err
		return nil, err
	}
	for _, a := range sortedKeys(p.assets) {
		if ext := path.Ext(a); ext == ".css" || ext == ".js" {
			if err := p.ensureAsset(p.assets[a]); err != nil {
				p.errs.add("assets/%s: %v", a, err)
			}
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
	os.MkdirAll(p.Out, 0o755)
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
					p.renderFailed(o.page, err)
					continue
				}
				r := p.toRecord(o, rec, html)
				var prev *OutRecord
				if old != nil {
					prev = old.Outputs[o.path]
				}
				if prev == nil || prev.Hash != r.Hash {
					wr.ch <- writeJob{dst: o.path, data: html, pooled: true}
					mu.Lock()
					written++
					mu.Unlock()
				} else {
					putBuf(html)
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
	for _, j := range p.planAssets(old, ns, files) {
		wr.ch <- j
	}
	res.Rendered = len(records)
	for _, o := range outs {
		if !dirty[o.path] {
			records[o.path] = old.Outputs[o.path]
			res.Reused++
		}
	}
	for _, j := range p.planCards(records, old, ns, files) {
		wr.ch <- j
	}
	wr.producers.Done()
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
	p.checkSVGs(old, ns)
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
	p.resolveIssues()
	t = phase("check", t)
	ns.OutID = dirID(p.Out)
	if err := b.saveState(ns); err != nil {
		p.errs.add("saving build state: %v", err)
	}
	if ss != nil {
		if err := b.saveScan(ss.save()); err != nil {
			p.errs.add("saving content cache: %v", err)
		}
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
		pg.card = newCard(pg)
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
	p.readKeys = make([]readKeys, len(p.maps))
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
		Pages: map[string]PageState{}, Files: map[string]string{}, AssetOut: map[string]string{}}
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
		s.Assets["asset:"+name] = xxh3.HashString(a.stamp)
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
		s.Pages[pageKey(pg)] = PageState{Meta: pg.metaHash, Body: body, Sort: pg.sortHash, File: pg.File, Group: pg.group}
	}
	return s
}

func gobHash(w io.Writer, v any) { w.Write(appendValueKey(nil, v)) }

func appendValueKey(b []byte, v any) []byte {
	switch t := v.(type) {
	case *yamlite.Map:
		b = append(b, '{')
		for i, k := range t.Keys {
			b = binary.AppendUvarint(b, uint64(len(k)))
			b = append(b, k...)
			b = appendValueKey(b, t.Vals[i])
		}
		return append(b, '}')
	case []any:
		b = append(b, '[')
		for _, e := range t {
			b = appendValueKey(b, e)
		}
		return append(b, ']')
	case string:
		b = append(b, 's')
		b = binary.AppendUvarint(b, uint64(len(t)))
		return append(b, t...)
	case int:
		return binary.AppendVarint(append(b, 'i'), int64(t))
	case float64:
		return binary.LittleEndian.AppendUint64(append(b, 'f'), math.Float64bits(t))
	case bool:
		if t {
			return append(b, 'T')
		}
		return append(b, 'F')
	}
	return fmt.Appendf(b, "%T:%v;", v, v)
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
		if !ok || op.Meta != ps.Meta || op.Group != ps.Group {
			pathChanged["*"+ps.Group] = true
			if ok {
				pathChanged["*"+op.Group] = true
			}
		}
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
	for k, op := range old.Pages {
		if _, ok := ns.Pages[k]; !ok {
			pathChanged["*"+op.Group] = true
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
		r.Reads = append(r.Reads, p.readKey(x))
	}
	for _, i := range rec.res {
		r.Res = append(r.Res, resKey(p.resources[i]))
	}
	r.Cards = keys(rec.cards)
	r.Metas = compactSorted(r.Metas)
	r.Scan = scanHTML(html)
	return r
}

type writeJob struct {
	dst    string
	src    string
	data   []byte
	res    *Resource
	svg    string
	pooled bool
}

type writers struct {
	ch        chan writeJob
	producers sync.WaitGroup
	done      sync.WaitGroup
	p         *Project
	dirs      sync.Map
}

func (w *writers) mkdir(dir string) {
	if dir == "." || dir == "/" || dir == "" {
		return
	}
	once, _ := w.dirs.LoadOrStore(dir, new(sync.Once))
	once.(*sync.Once).Do(func() {
		w.mkdir(path.Dir(dir))
		os.Mkdir(filepath.Join(w.p.Out, filepath.FromSlash(dir)), 0o755)
	})
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
				if j.svg != "" {
					p.svgChecked(j.svg, data)
				}
				w.mkdir(path.Dir(j.dst))
				if err := writeFile(filepath.Join(p.Out, filepath.FromSlash(j.dst)), data); err != nil {
					p.errs.add("%v", err)
				}
				if j.pooled {
					putBuf(data)
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
		src, dst, stamp, svg string
		res                  *Resource
	}
	var jobs []job
	for i, f := range p.static {
		jobs = append(jobs, job{filepath.Join(p.Root, "static", f), f, p.staticStamps[i], svgSource("static/" + f), nil})
	}
	for _, pg := range p.pages {
		for _, r := range pg.Resources {
			jobs = append(jobs, job{"", strings.TrimPrefix(r.RelPermalink, "/"), r.stamp, svgSource(r.src), r})
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
		todo = append(todo, writeJob{src: j.src, dst: j.dst, res: j.res, svg: j.svg})
	}
	return todo
}

func (p *Project) planAssets(old, ns *State, files map[string]bool) []writeJob {
	var todo []writeJob
	for _, name := range sortedKeys(p.assets) {
		a := p.assets[name]
		key := "asset:" + name
		if !a.processed {
			if old != nil && old.Assets[key] == ns.Assets[key] && old.AssetOut[name] != "" {
				out := old.AssetOut[name]
				files[out] = true
				ns.Files[out] = "asset"
				ns.AssetOut[name] = out
			}
			continue
		}
		files[a.outName] = true
		ns.Files[a.outName] = "asset"
		ns.AssetOut[name] = a.outName
		if old != nil && old.AssetOut[name] == a.outName && old.Files[a.outName] == "asset" {
			continue
		}
		todo = append(todo, writeJob{dst: a.outName, data: a.data, svg: svgSource(a.src)})
	}
	return todo
}

const ioWorkers = 6

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
