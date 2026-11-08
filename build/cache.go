package build

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/zeebo/xxh3"
)

var renderCacheDir = sync.OnceValue(func() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	d = filepath.Join(d, "lyn", strconv.FormatUint(xxh3.HashString(binaryID()), 36))
	if os.MkdirAll(d, 0o755) != nil {
		return ""
	}
	return d
})

func renderCached(kind string, input []byte, gen func() []byte) []byte {
	dir := renderCacheDir()
	if dir == "" {
		return gen()
	}
	h := xxh3.Hash128(input).Bytes()
	file := filepath.Join(dir, kind+"-"+strconv.FormatUint(xxh3.Hash(h[:]), 36))
	if b, err := os.ReadFile(file); err == nil && len(b) > 0 {
		return b
	}
	b := gen()
	if len(b) == 0 {
		return b
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return b
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
		os.Remove(tmp.Name())
	}
	return b
}
