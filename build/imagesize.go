package build

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

func rasterSize(data []byte) (int, int) {
	if w, h, ok := avifSize(data); ok {
		return w, h
	}
	if w, h, ok := webpSize(data); ok {
		return w, h
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return cfg.Width, cfg.Height
	}
	return 0, 0
}

func avifSize(b []byte) (int, int, bool) {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return 0, 0, false
	}
	brand := string(b[8:12])
	if brand != "avif" && brand != "avis" && !bytes.Contains(b[8:min(len(b), 64)], []byte("avif")) {
		return 0, 0, false
	}
	i := bytes.Index(b, []byte("ispe"))
	if i < 4 || i+16 > len(b) {
		return 0, 0, false
	}
	w := binary.BigEndian.Uint32(b[i+8:])
	h := binary.BigEndian.Uint32(b[i+12:])
	return int(w), int(h), w > 0 && h > 0
}

func webpSize(b []byte) (int, int, bool) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8 ":
		w := int(binary.LittleEndian.Uint16(b[26:]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(b[28:]) & 0x3fff)
		return w, h, true
	case "VP8L":
		bits := binary.LittleEndian.Uint32(b[21:])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, true
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, true
	}
	return 0, 0, false
}
