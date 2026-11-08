package build

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zeebo/xxh3"
)

const scanMagic = "lyn-content\n"

type scanCache struct {
	dirs  map[string]cachedDir
	files map[string]cachedFile
	size  int
}

type cachedDir struct {
	start   int64
	stamp   string
	entries []cachedEntry
}

type cachedFile struct {
	start int64
	stamp string
	data  []byte
}

type cachedEntry struct {
	name string
	typ  fs.FileMode
	dir  string
}

func (e cachedEntry) Name() string      { return e.name }
func (e cachedEntry) IsDir() bool       { return e.typ.IsDir() }
func (e cachedEntry) Type() fs.FileMode { return e.typ }
func (e cachedEntry) Info() (fs.FileInfo, error) {
	return os.Lstat(e.dir + string(filepath.Separator) + e.name)
}

type scanState struct {
	prev       *scanCache
	next       *scanCache
	start      int64
	mu         sync.Mutex
	freshDirs  []string
	freshFiles []string
}

func newScanState(prev *scanCache) *scanState {
	return &scanState{prev: prev, start: time.Now().UnixNano(), next: &scanCache{dirs: map[string]cachedDir{}, files: map[string]cachedFile{}}}
}

func scanStamp(fi fs.FileInfo) string {
	ino := uint64(0)
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = uint64(st.Ino)
	}
	return strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatUint(ino, 10)
}

func trusted(fi fs.FileInfo, start int64) bool {
	return fi.ModTime().UnixNano() < start-int64(time.Second)
}

func (s *scanState) readDir(rel, full string) ([]fs.DirEntry, error) {
	var old cachedDir
	var had bool
	if s.prev != nil {
		old, had = s.prev.dirs[rel]
	}
	if had {
		fi, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		if trusted(fi, old.start) && scanStamp(fi) == old.stamp {
			out := make([]fs.DirEntry, len(old.entries))
			for i, e := range old.entries {
				out[i] = e
			}
			s.mu.Lock()
			s.next.dirs[rel] = old
			s.mu.Unlock()
			return out, nil
		}
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	list, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	d := cachedDir{start: s.start, stamp: scanStamp(fi), entries: make([]cachedEntry, len(list))}
	for i, e := range list {
		d.entries[i] = cachedEntry{name: e.Name(), typ: e.Type(), dir: full}
	}
	s.mu.Lock()
	s.next.dirs[rel] = d
	s.freshDirs = append(s.freshDirs, rel)
	s.mu.Unlock()
	return list, nil
}

func (s *scanState) readFile(rel, full string) ([]byte, error) {
	var old cachedFile
	var had bool
	if s.prev != nil {
		old, had = s.prev.files[rel]
	}
	if had {
		fi, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		if trusted(fi, old.start) && scanStamp(fi) == old.stamp {
			s.mu.Lock()
			s.next.files[rel] = old
			s.mu.Unlock()
			return old.data, nil
		}
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, max(int(fi.Size())+1, 512))
	for {
		n, err := f.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(data) == cap(data) {
			data = append(data, 0)[:len(data)]
		}
	}
	s.mu.Lock()
	s.next.files[rel] = cachedFile{start: s.start, stamp: scanStamp(fi), data: data}
	s.freshFiles = append(s.freshFiles, rel)
	s.mu.Unlock()
	return data, nil
}

func (s *scanState) derived(key string, compute func() ([]byte, error)) ([]byte, error) {
	if s == nil {
		return compute()
	}
	if s.prev != nil {
		if f, ok := s.prev.files[key]; ok {
			s.mu.Lock()
			s.next.files[key] = f
			s.mu.Unlock()
			return f.data, nil
		}
	}
	data, err := compute()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.next.files[key] = cachedFile{start: s.start, data: data}
	s.freshFiles = append(s.freshFiles, key)
	s.mu.Unlock()
	return data, nil
}

func (p *Project) minify(mediaType string, src []byte) ([]byte, error) {
	sum := xxh3.Hash128(src).Bytes()
	return p.cache.scan.derived("minified:"+mediaType+":"+hex.EncodeToString(sum[:]), func() ([]byte, error) {
		var out bytes.Buffer
		err := minifier.Minify(mediaType, &out, bytes.NewReader(src))
		return out.Bytes(), err
	})
}

type scanSave struct {
	cache   *scanCache
	segment []byte
	full    bool
}

func (s *scanState) save() *scanSave {
	var goneDirs, goneFiles []string
	if s.prev != nil {
		for rel := range s.prev.dirs {
			if _, ok := s.next.dirs[rel]; !ok {
				goneDirs = append(goneDirs, rel)
			}
		}
		for rel := range s.prev.files {
			if _, ok := s.next.files[rel]; !ok {
				goneFiles = append(goneFiles, rel)
			}
		}
	}
	if s.prev != nil && len(s.freshDirs)+len(s.freshFiles)+len(goneDirs)+len(goneFiles) == 0 {
		return nil
	}
	if s.prev == nil {
		s.freshDirs, s.freshFiles = keysOf(s.next.dirs), keysOf(s.next.files)
	}
	seg := encodeSegment(s.next, s.freshDirs, s.freshFiles, goneDirs, goneFiles)
	if s.prev != nil {
		s.next.size = s.prev.size + len(seg)
		live := 0
		for _, f := range s.next.files {
			live += len(f.data) + 64
		}
		live += 64 * len(s.next.dirs)
		if s.next.size < 2*live+1<<20 {
			return &scanSave{cache: s.next, segment: seg}
		}
	}
	seg = encodeSegment(s.next, keysOf(s.next.dirs), keysOf(s.next.files), nil, nil)
	s.next.size = len(scanMagic) + len(seg)
	return &scanSave{cache: s.next, segment: seg, full: true}
}

func encodeSegment(c *scanCache, dirs, files, goneDirs, goneFiles []string) []byte {
	size := 4 * binary.MaxVarintLen64
	for _, rel := range dirs {
		d := c.dirs[rel]
		size += 4*binary.MaxVarintLen64 + len(rel) + len(d.stamp)
		for _, e := range d.entries {
			size += 2*binary.MaxVarintLen64 + len(e.name)
		}
	}
	for _, rel := range files {
		f := c.files[rel]
		size += 4*binary.MaxVarintLen64 + len(rel) + len(f.stamp) + len(f.data)
	}
	for _, rel := range slices.Concat(goneDirs, goneFiles) {
		size += binary.MaxVarintLen64 + len(rel)
	}
	b := make([]byte, 16, 16+size)
	str := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	b = binary.AppendUvarint(b, uint64(len(dirs)))
	for _, rel := range dirs {
		d := c.dirs[rel]
		str(rel)
		b = binary.AppendVarint(b, d.start)
		str(d.stamp)
		b = binary.AppendUvarint(b, uint64(len(d.entries)))
		for _, e := range d.entries {
			str(e.name)
			b = binary.AppendUvarint(b, uint64(e.typ))
		}
	}
	b = binary.AppendUvarint(b, uint64(len(files)))
	for _, rel := range files {
		f := c.files[rel]
		str(rel)
		b = binary.AppendVarint(b, f.start)
		str(f.stamp)
		b = binary.AppendUvarint(b, uint64(len(f.data)))
		b = append(b, f.data...)
	}
	for _, gone := range [][]string{goneDirs, goneFiles} {
		b = binary.AppendUvarint(b, uint64(len(gone)))
		for _, rel := range gone {
			str(rel)
		}
	}
	binary.LittleEndian.PutUint64(b[0:8], uint64(len(b)-16))
	binary.LittleEndian.PutUint64(b[8:16], xxh3.Hash(b[16:]))
	return b
}

var errScan = errors.New("invalid content cache")

func decodeScan(root string, file []byte) (*scanCache, error) {
	if len(file) < len(scanMagic) || string(file[:len(scanMagic)]) != scanMagic {
		return nil, errScan
	}
	c := &scanCache{dirs: map[string]cachedDir{}, files: map[string]cachedFile{}, size: len(file)}
	rest := file[len(scanMagic):]
	for len(rest) > 0 {
		if len(rest) < 16 {
			return nil, errScan
		}
		n := binary.LittleEndian.Uint64(rest[0:8])
		if n > uint64(len(rest)-16) || xxh3.Hash(rest[16:16+n]) != binary.LittleEndian.Uint64(rest[8:16]) {
			return nil, errScan
		}
		if err := decodeSegment(c, root, rest[16:16+n]); err != nil {
			return nil, err
		}
		rest = rest[16+n:]
	}
	return c, nil
}

func decodeSegment(c *scanCache, root string, b []byte) error {
	bad := false
	num := func() uint64 {
		x, n := binary.Uvarint(b)
		if n <= 0 {
			bad = true
			b = nil
			return 0
		}
		b = b[n:]
		return x
	}
	signed := func() int64 {
		x, n := binary.Varint(b)
		if n <= 0 {
			bad = true
			b = nil
			return 0
		}
		b = b[n:]
		return x
	}
	raw := func() []byte {
		n := num()
		if n > uint64(len(b)) {
			bad = true
			b = nil
			return nil
		}
		out := b[:n:n]
		b = b[n:]
		return out
	}
	count := func() uint64 {
		n := num()
		if n > uint64(len(b)) {
			bad = true
			b = nil
			return 0
		}
		return n
	}
	for range count() {
		rel := string(raw())
		d := cachedDir{start: signed(), stamp: string(raw())}
		full := filepath.Join(root, filepath.FromSlash(rel))
		d.entries = make([]cachedEntry, count())
		for i := range d.entries {
			name := string(raw())
			d.entries[i] = cachedEntry{name: name, typ: fs.FileMode(num()), dir: full}
		}
		c.dirs[rel] = d
	}
	for range count() {
		rel := string(raw())
		f := cachedFile{start: signed(), stamp: string(raw())}
		f.data = raw()
		c.files[rel] = f
	}
	for range count() {
		delete(c.dirs, string(raw()))
	}
	for range count() {
		delete(c.files, string(raw()))
	}
	if bad || len(b) != 0 {
		return errScan
	}
	return nil
}
