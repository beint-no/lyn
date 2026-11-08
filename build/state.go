package build

import (
	"encoding/binary"
	"errors"
	"slices"

	"github.com/zeebo/xxh3"
)

const stateMagic = "lyn-state\n"

type stateWriter struct {
	buf  []byte
	ids  map[string]uint64
	strs []string
	refs int
}

func (w *stateWriter) uint(x uint64) { w.buf = binary.AppendUvarint(w.buf, x) }

func (w *stateWriter) int(x int) { w.buf = binary.AppendVarint(w.buf, int64(x)) }

func (w *stateWriter) bool(x bool) {
	if x {
		w.buf = append(w.buf, 1)
	} else {
		w.buf = append(w.buf, 0)
	}
}

func (w *stateWriter) str(s string) {
	id, ok := w.ids[s]
	if !ok {
		id = uint64(len(w.strs))
		w.ids[s] = id
		w.strs = append(w.strs, s)
	}
	w.uint(id)
}

func (w *stateWriter) list(l []string) {
	w.uint(uint64(len(l)))
	w.refs += len(l)
	for _, s := range l {
		w.str(s)
	}
}

func (w *stateWriter) hashes(m map[string]uint64) {
	w.uint(uint64(len(m)))
	for k, v := range m {
		w.str(k)
		w.uint(v)
	}
}

func (w *stateWriter) names(m map[string]string) {
	w.uint(uint64(len(m)))
	for k, v := range m {
		w.str(k)
		w.str(v)
	}
}

func encodeState(s *State) []byte {
	return append([]byte(stateMagic), encodeStateSegment(s, keysOf(s.Outputs), nil)...)
}

func encodeStateSegment(s *State, outputs, removed []string) []byte {
	w := &stateWriter{ids: map[string]uint64{}, buf: make([]byte, 0, 1<<16)}
	w.int(s.Version)
	w.str(s.Binary)
	w.uint(s.Config)
	w.int(s.Year)
	w.hashes(s.Templates)
	w.hashes(s.I18n)
	w.hashes(s.Data)
	w.hashes(s.Assets)
	w.hashes(s.Statics)
	w.uint(uint64(len(s.Pages)))
	for k, ps := range s.Pages {
		w.str(k)
		w.uint(ps.Meta)
		w.uint(ps.Body)
		w.uint(ps.Sort)
		w.str(ps.File)
		w.str(ps.Group)
		w.list(ps.Shortcodes)
	}
	w.uint(uint64(len(outputs)))
	for _, k := range outputs {
		r := s.Outputs[k]
		w.str(k)
		w.str(r.Page)
		w.uint(r.Hash)
		w.list(r.Templates)
		w.list(r.Metas)
		w.list(r.Bodies)
		w.list(r.Colls)
		w.list(r.RColls)
		w.list(r.Paths)
		w.list(r.I18n)
		w.list(r.Reads)
		w.list(r.Res)
		w.list(r.Cards)
		w.bool(r.Year)
		sc := &r.Scan
		w.list(sc.Links)
		w.list(sc.IDs)
		w.list(sc.Classes)
		w.list(sc.Data)
		w.list(sc.VarDefs)
		w.list(sc.VarUses)
		w.list(sc.VarBare)
		w.list(sc.Words)
		w.str(sc.Title)
		w.int(sc.Titles)
		w.str(sc.Desc)
		w.bool(sc.NoIndex)
		w.uint(uint64(len(sc.Errs)))
		for _, e := range sc.Errs {
			w.int(e.Off)
			w.str(e.Msg)
			w.list(e.Words)
		}
		w.int(sc.Imgs)
		w.int(sc.H1)
		w.int(sc.H1Off)
		w.list(sc.Canon)
		w.str(sc.OGURL)
		w.list(sc.Alts)
		w.list(sc.ImgSizes)
		w.list(sc.ScriptIDs)
	}
	w.list(removed)
	w.names(s.Files)
	w.names(s.AssetOut)
	w.names(s.SVGs)
	w.names(s.ImgDims)
	w.str(s.OutID)

	size := 16 + 2*binary.MaxVarintLen64 + len(w.buf)
	for _, x := range w.strs {
		size += binary.MaxVarintLen64 + len(x)
	}
	out := make([]byte, 16, size)
	out = binary.AppendUvarint(out, uint64(len(w.strs)))
	out = binary.AppendUvarint(out, uint64(w.refs))
	for _, x := range w.strs {
		out = binary.AppendUvarint(out, uint64(len(x)))
	}
	for _, x := range w.strs {
		out = append(out, x...)
	}
	out = append(out, w.buf...)
	binary.LittleEndian.PutUint64(out[0:8], uint64(len(out)-16))
	binary.LittleEndian.PutUint64(out[8:16], xxh3.Hash(out[16:]))
	return out
}

var errState = errors.New("invalid state")

type stateReader struct {
	b    []byte
	strs []string
	refs []string
	err  error
}

func (r *stateReader) uint() uint64 {
	x, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errState
		r.b = nil
		return 0
	}
	r.b = r.b[n:]
	return x
}

func (r *stateReader) int() int {
	x, n := binary.Varint(r.b)
	if n <= 0 {
		r.err = errState
		r.b = nil
		return 0
	}
	r.b = r.b[n:]
	return int(x)
}

func (r *stateReader) bool() bool {
	if len(r.b) == 0 {
		r.err = errState
		return false
	}
	x := r.b[0]
	r.b = r.b[1:]
	return x == 1
}

func (r *stateReader) count() int {
	n := r.uint()
	if n > uint64(len(r.b)) {
		r.err = errState
		r.b = nil
		return 0
	}
	return int(n)
}

func (r *stateReader) str() string {
	id := r.uint()
	if id >= uint64(len(r.strs)) {
		r.err = errState
		r.b = nil
		return ""
	}
	return r.strs[id]
}

func (r *stateReader) list() []string {
	n := r.count()
	if n == 0 {
		return nil
	}
	if n > len(r.refs) {
		r.err = errState
		r.b = nil
		return nil
	}
	l := r.refs[:n:n]
	r.refs = r.refs[n:]
	for i := range l {
		l[i] = r.str()
	}
	return l
}

func (r *stateReader) hashes() map[string]uint64 {
	n := r.count()
	m := make(map[string]uint64, n)
	for range n {
		k := r.str()
		m[k] = r.uint()
	}
	return m
}

func (r *stateReader) names() map[string]string {
	n := r.count()
	m := make(map[string]string, n)
	for range n {
		k := r.str()
		m[k] = r.str()
	}
	return m
}

func decodeState(file []byte) (*State, int, error) {
	if len(file) < len(stateMagic) || string(file[:len(stateMagic)]) != stateMagic {
		return nil, 0, errState
	}
	var s *State
	base := 0
	rest := file[len(stateMagic):]
	for len(rest) > 0 {
		if len(rest) < 16 {
			return nil, 0, errState
		}
		n := binary.LittleEndian.Uint64(rest[0:8])
		if n > uint64(len(rest)-16) || xxh3.Hash(rest[16:16+n]) != binary.LittleEndian.Uint64(rest[8:16]) {
			return nil, 0, errState
		}
		next, err := decodeStateSegment(s, rest[16:16+n])
		if err != nil {
			return nil, 0, err
		}
		if s == nil {
			base = len(stateMagic) + 16 + int(n)
		}
		s = next
		rest = rest[16+n:]
	}
	if s == nil {
		return nil, 0, errState
	}
	return s, base, nil
}

func decodeStateSegment(prev *State, b []byte) (*State, error) {
	r := &stateReader{b: b}
	nstr := r.count()
	nref := r.uint()
	if r.err != nil || nref > uint64(len(b)) {
		return nil, errState
	}
	lens := make([]int, nstr)
	total := 0
	for i := range lens {
		lens[i] = r.count()
		total += lens[i]
	}
	if r.err != nil || total > len(r.b) {
		return nil, errState
	}
	all := string(r.b[:total])
	r.b = r.b[total:]
	r.strs = make([]string, nstr)
	off := 0
	for i, n := range lens {
		r.strs[i] = all[off : off+n]
		off += n
	}
	r.refs = make([]string, nref)
	s := &State{}
	s.Version = r.int()
	s.Binary = r.str()
	s.Config = r.uint()
	s.Year = r.int()
	s.Templates = r.hashes()
	s.I18n = r.hashes()
	s.Data = r.hashes()
	s.Assets = r.hashes()
	s.Statics = r.hashes()
	n := r.count()
	s.Pages = make(map[string]PageState, n)
	for range n {
		k := r.str()
		var ps PageState
		ps.Meta = r.uint()
		ps.Body = r.uint()
		ps.Sort = r.uint()
		ps.File = r.str()
		ps.Group = r.str()
		ps.Shortcodes = r.list()
		s.Pages[k] = ps
	}
	n = r.count()
	if prev != nil {
		s.Outputs = prev.Outputs
	} else {
		s.Outputs = make(map[string]*OutRecord, n)
	}
	recs := make([]OutRecord, n)
	for i := range n {
		k := r.str()
		o := &recs[i]
		o.Page = r.str()
		o.Hash = r.uint()
		o.Templates = r.list()
		o.Metas = r.list()
		o.Bodies = r.list()
		o.Colls = r.list()
		o.RColls = r.list()
		o.Paths = r.list()
		o.I18n = r.list()
		o.Reads = r.list()
		o.Res = r.list()
		o.Cards = r.list()
		o.Year = r.bool()
		sc := &o.Scan
		sc.Links = r.list()
		sc.IDs = r.list()
		sc.Classes = r.list()
		sc.Data = r.list()
		sc.VarDefs = r.list()
		sc.VarUses = r.list()
		sc.VarBare = r.list()
		sc.Words = r.list()
		sc.Title = r.str()
		sc.Titles = r.int()
		sc.Desc = r.str()
		sc.NoIndex = r.bool()
		if n := r.count(); n > 0 {
			sc.Errs = make([]ScanErr, n)
			for i := range sc.Errs {
				sc.Errs[i].Off = r.int()
				sc.Errs[i].Msg = r.str()
				sc.Errs[i].Words = r.list()
			}
		}
		sc.Imgs = r.int()
		sc.H1 = r.int()
		sc.H1Off = r.int()
		sc.Canon = r.list()
		sc.OGURL = r.str()
		sc.Alts = r.list()
		sc.ImgSizes = r.list()
		sc.ScriptIDs = r.list()
		s.Outputs[k] = o
	}
	for _, k := range r.list() {
		delete(s.Outputs, k)
	}
	s.Files = r.names()
	s.AssetOut = r.names()
	s.SVGs = r.names()
	s.ImgDims = r.names()
	s.OutID = r.str()
	if r.err != nil || len(r.b) != 0 {
		return nil, errState
	}
	return s, nil
}

func sameState(a, b *State) bool {
	if a.Version != b.Version || a.Binary != b.Binary || a.Config != b.Config || a.Year != b.Year || a.OutID != b.OutID {
		return false
	}
	for _, pair := range [][2]map[string]uint64{{a.Templates, b.Templates}, {a.I18n, b.I18n}, {a.Data, b.Data}, {a.Assets, b.Assets}, {a.Statics, b.Statics}} {
		if !sameMap(pair[0], pair[1], func(x, y uint64) bool { return x == y }) {
			return false
		}
	}
	return sameMap(a.Files, b.Files, func(x, y string) bool { return x == y }) &&
		sameMap(a.AssetOut, b.AssetOut, func(x, y string) bool { return x == y }) &&
		sameMap(a.SVGs, b.SVGs, func(x, y string) bool { return x == y }) &&
		sameMap(a.ImgDims, b.ImgDims, func(x, y string) bool { return x == y }) &&
		sameMap(a.Pages, b.Pages, func(x, y PageState) bool {
			return x.Meta == y.Meta && x.Body == y.Body && x.Sort == y.Sort && x.File == y.File && x.Group == y.Group && slices.Equal(x.Shortcodes, y.Shortcodes)
		}) &&
		sameMap(a.Outputs, b.Outputs, func(x, y *OutRecord) bool { return x == y })
}

func sameMap[V any](a, b map[string]V, eq func(x, y V) bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || !eq(x, y) {
			return false
		}
	}
	return true
}
