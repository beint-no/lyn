package build

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const scanMagic = "lyn-content\n"

type scanCache struct {
	start int64
	dirs  map[string]cachedDir
	files map[string]cachedFile
}

type cachedDir struct {
	stamp   string
	entries []cachedEntry
}

type cachedFile struct {
	stamp string
	data  []byte
}

type cachedEntry struct {
	name string
	typ  fs.FileMode
	full string
}

func (e cachedEntry) Name() string               { return e.name }
func (e cachedEntry) IsDir() bool                { return e.typ.IsDir() }
func (e cachedEntry) Type() fs.FileMode          { return e.typ }
func (e cachedEntry) Info() (fs.FileInfo, error) { return os.Lstat(e.full) }

type scanState struct {
	prev  *scanCache
	next  *scanCache
	mu    sync.Mutex
	dirty bool
}

func newScanState(prev *scanCache) *scanState {
	return &scanState{prev: prev, next: &scanCache{start: time.Now().UnixNano(), dirs: map[string]cachedDir{}, files: map[string]cachedFile{}}}
}

func scanStamp(fi fs.FileInfo) string {
	ino := uint64(0)
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = uint64(st.Ino)
	}
	return strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatUint(ino, 10)
}

func (s *scanState) trusted(fi fs.FileInfo) bool {
	return s.prev != nil && fi.ModTime().UnixNano() < s.prev.start-int64(time.Second)
}

func (s *scanState) readDir(rel, full string) ([]fs.DirEntry, error) {
	fi, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	stamp := scanStamp(fi)
	if s.trusted(fi) {
		if d, ok := s.prev.dirs[rel]; ok && d.stamp == stamp {
			out := make([]fs.DirEntry, len(d.entries))
			for i, e := range d.entries {
				out[i] = e
			}
			s.mu.Lock()
			s.next.dirs[rel] = d
			s.mu.Unlock()
			return out, nil
		}
	}
	list, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	d := cachedDir{stamp: stamp, entries: make([]cachedEntry, len(list))}
	for i, e := range list {
		d.entries[i] = cachedEntry{name: e.Name(), typ: e.Type(), full: filepath.Join(full, e.Name())}
	}
	s.mu.Lock()
	s.next.dirs[rel] = d
	s.dirty = true
	s.mu.Unlock()
	return list, nil
}

func (s *scanState) readFile(rel, full string) ([]byte, error) {
	fi, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	stamp := scanStamp(fi)
	var old cachedFile
	var had bool
	if s.prev != nil {
		old, had = s.prev.files[rel]
	}
	if had && old.stamp == stamp && s.trusted(fi) {
		s.mu.Lock()
		s.next.files[rel] = old
		s.mu.Unlock()
		return old.data, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.next.files[rel] = cachedFile{stamp: stamp, data: data}
	if !had || old.stamp != stamp || string(old.data) != string(data) {
		s.dirty = true
	}
	s.mu.Unlock()
	return data, nil
}

func (s *scanState) changed() bool {
	return s.dirty || s.prev == nil || len(s.prev.dirs) != len(s.next.dirs) || len(s.prev.files) != len(s.next.files)
}

func encodeScan(c *scanCache) []byte {
	size := len(scanMagic) + binary.MaxVarintLen64
	for rel, d := range c.dirs {
		size += 3*binary.MaxVarintLen64 + len(rel) + len(d.stamp)
		for _, e := range d.entries {
			size += 2*binary.MaxVarintLen64 + len(e.name)
		}
	}
	for rel, f := range c.files {
		size += 3*binary.MaxVarintLen64 + len(rel) + len(f.stamp) + len(f.data)
	}
	b := make([]byte, 0, size)
	b = append(b, scanMagic...)
	b = binary.AppendVarint(b, c.start)
	str := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	b = binary.AppendUvarint(b, uint64(len(c.dirs)))
	for rel, d := range c.dirs {
		str(rel)
		str(d.stamp)
		b = binary.AppendUvarint(b, uint64(len(d.entries)))
		for _, e := range d.entries {
			str(e.name)
			b = binary.AppendUvarint(b, uint64(e.typ))
		}
	}
	b = binary.AppendUvarint(b, uint64(len(c.files)))
	for rel, f := range c.files {
		str(rel)
		str(f.stamp)
		b = binary.AppendUvarint(b, uint64(len(f.data)))
		b = append(b, f.data...)
	}
	return b
}

var errScan = errors.New("invalid content cache")

func decodeScan(root string, b []byte) (*scanCache, error) {
	if len(b) < len(scanMagic) || string(b[:len(scanMagic)]) != scanMagic {
		return nil, errScan
	}
	b = b[len(scanMagic):]
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
	start, n := binary.Varint(b)
	if n <= 0 {
		return nil, errScan
	}
	b = b[n:]
	c := &scanCache{start: start}
	nd := num()
	if nd > uint64(len(b)) {
		return nil, errScan
	}
	c.dirs = make(map[string]cachedDir, nd)
	for range nd {
		rel := string(raw())
		d := cachedDir{stamp: string(raw())}
		ne := num()
		if ne > uint64(len(b)) {
			return nil, errScan
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		d.entries = make([]cachedEntry, ne)
		for i := range d.entries {
			name := string(raw())
			d.entries[i] = cachedEntry{name: name, typ: fs.FileMode(num()), full: filepath.Join(full, name)}
		}
		c.dirs[rel] = d
	}
	nf := num()
	if nf > uint64(len(b)) {
		return nil, errScan
	}
	c.files = make(map[string]cachedFile, nf)
	for range nf {
		rel := string(raw())
		f := cachedFile{stamp: string(raw())}
		f.data = raw()
		c.files[rel] = f
	}
	if bad || len(b) != 0 {
		return nil, errScan
	}
	return c, nil
}
