package build

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

//go:embed fonts/Inter-Regular.ttf
var interRegular []byte

//go:embed fonts/Inter-Bold.ttf
var interBold []byte

const (
	faceTable   = 0x2200
	faceAscent  = 0.969
	faceDescent = 0.242
)

type face struct {
	f    *sfnt.Font
	upm  fixed.Int26_6
	adv  []float32
	none float32
}

var faces = sync.OnceValue(func() [2]*face {
	return [2]*face{loadFace(interRegular), loadFace(interBold)}
})

func fontFace(bold bool) *face {
	if bold {
		return faces()[1]
	}
	return faces()[0]
}

func loadFace(b []byte) *face {
	f, err := sfnt.Parse(b)
	if err != nil {
		panic(err)
	}
	var buf sfnt.Buffer
	fc := &face{f: f, upm: fixed.Int26_6(f.UnitsPerEm()) << 6, adv: make([]float32, faceTable)}
	em := func(i sfnt.GlyphIndex) float32 {
		a, _ := f.GlyphAdvance(&buf, i, fc.upm, font.HintingNone)
		return float32(a) / float32(fc.upm)
	}
	fc.none = em(0)
	for r := range rune(faceTable) {
		fc.adv[r] = fc.none
		if i, _ := f.GlyphIndex(&buf, r); i != 0 {
			fc.adv[r] = em(i)
		}
	}
	return fc
}

func (fc *face) advance(r rune) float64 {
	if r >= 0 && r < faceTable {
		return float64(fc.adv[r])
	}
	return float64(fc.none)
}

func (fc *face) check(s string) error {
	var buf sfnt.Buffer
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if i, _ := fc.f.GlyphIndex(&buf, r); i == 0 {
			return fmt.Errorf("the og-image font (Inter, Latin) has no glyph for %q", r)
		}
	}
	return nil
}

func (fc *face) measure(s string, px float64) float64 {
	var buf sfnt.Buffer
	ppem := fixed.Int26_6(math.Round(px * 64))
	var w fixed.Int26_6
	var prev sfnt.GlyphIndex
	for _, r := range s {
		i, _ := fc.f.GlyphIndex(&buf, r)
		if prev != 0 {
			k, _ := fc.f.Kern(&buf, prev, i, ppem, font.HintingNone)
			w += k
		}
		a, _ := fc.f.GlyphAdvance(&buf, i, ppem, font.HintingNone)
		w += a
		prev = i
	}
	return float64(w) / 64
}

func (fc *face) draw(dst *image.RGBA, s string, x, baseline, px float64, c color.NRGBA) {
	var buf sfnt.Buffer
	ppem := fixed.Int26_6(math.Round(px * 64))
	ox, oy := int(math.Floor(x))-2, int(math.Floor(baseline-px*faceAscent))-2
	w, h := int(math.Ceil(fc.measure(s, px)))+6, int(math.Ceil(px*(faceAscent+faceDescent)))+6
	z := vector.NewRasterizer(w, h)
	pen := x - float64(ox)
	by := baseline - float64(oy)
	pt := func(p fixed.Point26_6) (float32, float32) {
		return float32(pen + float64(p.X)/64), float32(by + float64(p.Y)/64)
	}
	var prev sfnt.GlyphIndex
	for _, r := range s {
		i, _ := fc.f.GlyphIndex(&buf, r)
		if prev != 0 {
			k, _ := fc.f.Kern(&buf, prev, i, ppem, font.HintingNone)
			pen += float64(k) / 64
		}
		segs, _ := fc.f.LoadGlyph(&buf, i, ppem, nil)
		for _, sg := range segs {
			switch sg.Op {
			case sfnt.SegmentOpMoveTo:
				z.MoveTo(pt(sg.Args[0]))
			case sfnt.SegmentOpLineTo:
				z.LineTo(pt(sg.Args[0]))
			case sfnt.SegmentOpQuadTo:
				ax, ay := pt(sg.Args[0])
				bx, by := pt(sg.Args[1])
				z.QuadTo(ax, ay, bx, by)
			case sfnt.SegmentOpCubeTo:
				ax, ay := pt(sg.Args[0])
				bx, by := pt(sg.Args[1])
				cx, cy := pt(sg.Args[2])
				z.CubeTo(ax, ay, bx, by, cx, cy)
			}
		}
		a, _ := fc.f.GlyphAdvance(&buf, i, ppem, font.HintingNone)
		pen += float64(a) / 64
		prev = i
	}
	z.ClosePath()
	z.Draw(dst, image.Rect(ox, oy, ox+w, oy+h), image.NewUniform(c), image.Point{})
}

func wrapLines(text string, maxLines int, fits func(string) bool) []string {
	words := strings.Fields(text)
	if len(words) == 0 || maxLines <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for i, w := range words {
		next := w
		if cur != "" {
			next = cur + " " + w
		}
		if cur == "" || fits(next) {
			cur = next
			continue
		}
		if len(lines) == maxLines-1 {
			return append(lines, ellipsize(cur+" "+strings.Join(words[i:], " "), fits))
		}
		lines = append(lines, cur)
		cur = w
	}
	if !fits(cur) {
		cur = ellipsize(cur+" …", fits)
	}
	return append(lines, cur)
}

func ellipsize(s string, fits func(string) bool) string {
	words := strings.Fields(s)
	for n := len(words) - 1; n > 0; n-- {
		cand := strings.TrimSpace(strings.TrimRight(strings.Join(words[:n], " "), ",;:–—-|")) + " …"
		if fits(cand) {
			return cand
		}
	}
	r := []rune(words[0])
	for n := len(r) - 1; n > 0; n-- {
		if cand := string(r[:n]) + "…"; fits(cand) {
			return cand
		}
	}
	return "…"
}
