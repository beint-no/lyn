package build

import (
	"encoding/binary"
	"testing"
)

func TestRasterHeaders(t *testing.T) {
	avif := []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")
	ispe := make([]byte, 20)
	binary.BigEndian.PutUint32(ispe[0:], 20)
	copy(ispe[4:], "ispe")
	binary.BigEndian.PutUint32(ispe[12:], 1200)
	binary.BigEndian.PutUint32(ispe[16:], 714)
	avif = append(avif, ispe...)
	if w, h := rasterSize(avif); w != 1200 || h != 714 {
		t.Fatalf("avif %d×%d", w, h)
	}
	vp8x := make([]byte, 30)
	copy(vp8x, "RIFF\x00\x00\x00\x00WEBPVP8X")
	vp8x[24], vp8x[25] = 0xbf, 0x03
	vp8x[27], vp8x[28] = 0x1b, 0x02
	if w, h := rasterSize(vp8x); w != 960 || h != 540 {
		t.Fatalf("webp %d×%d", w, h)
	}
}
