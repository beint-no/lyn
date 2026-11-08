package build

import (
	"bufio"
	"io"
	"sync"
)

var outBufs sync.Pool

func getBuf(size int) []byte {
	if b, ok := outBufs.Get().(*[]byte); ok {
		return (*b)[:0]
	}
	if size == 0 {
		return nil
	}
	return make([]byte, 0, size)
}

func putBuf(b []byte) {
	if cap(b) == 0 || cap(b) > 1<<20 {
		return
	}
	outBufs.Put(&b)
}

var bufWriters = sync.Pool{New: func() any { return bufio.NewWriterSize(nil, 4096) }}

func getWriter(w io.Writer) *bufio.Writer {
	bw := bufWriters.Get().(*bufio.Writer)
	bw.Reset(w)
	return bw
}

func putWriter(bw *bufio.Writer) {
	bw.Reset(nil)
	bufWriters.Put(bw)
}
