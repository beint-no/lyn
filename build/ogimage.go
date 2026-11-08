package build

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"hash/adler32"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"github.com/zeebo/xxh3"
	"golang.org/x/text/unicode/norm"
)

const (
	ogName      = "og-image.png"
	ogW         = 1200
	ogH         = 630
	ogMargin    = 88
	ogCol       = 640
	ogIcon      = 340
	ogIconX     = 776
	ogBar       = 18
	ogHostPx    = 26
	ogHostBase  = 100
	ogTitlePx   = 90
	ogTitleMin  = 48
	ogTitleBase = 208
	ogTagPx     = 38
	ogTagBase   = 290
	ogTagStep   = 50
	ogTagLines  = 5
	ogShadow    = 16
	ogShadowDY  = 18
	ogTint      = 0.07
	ogInkAccent = 0.14
)

var brandSeparators = []string{" | ", " – ", " — ", " - "}

func brand(title string) string {
	cut := len(title)
	for _, sep := range brandSeparators {
		if i := strings.Index(title, sep); i > 0 && i < cut {
			cut = i
		}
	}
	return title[:cut]
}

var plainMD = goldmark.New(goldmark.WithExtensions(extension.Typographer))

func inlinePlain(s string) string {
	src := []byte(inlineOnly(s))
	return strings.Join(strings.Fields(plainText(plainMD.Parser().Parse(text.NewReader(src)), src)), " ")
}

type ogText struct {
	host, title, tagline, from string
}

func (p *Project) ogText() ogText {
	t := ogText{title: brand(strings.Join(strings.Fields(p.Title), " ")), from: "site.yml"}
	if u, err := url.Parse(p.BaseURL); err == nil {
		t.host = u.Hostname()
	}
	if home := p.Sites[0].Home; home != nil {
		if home.Summary != "" {
			t.tagline, t.from = inlinePlain(home.Summary), p.fmLoc(home, "summary")
		} else {
			t.tagline, t.from = strings.Join(strings.Fields(home.Description), " "), p.fmLoc(home, "description")
		}
	}
	t.host, t.title, t.tagline = norm.NFC.String(t.host), norm.NFC.String(t.title), norm.NFC.String(t.tagline)
	return t
}

func luminance(c color.NRGBA) float64 {
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

func contrast(a, b color.NRGBA) float64 {
	x, y := luminance(a)+0.05, luminance(b)+0.05
	return max(x, y) / min(x, y)
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	f := func(p, q uint8) uint8 { return uint8(math.Round(float64(p) + (float64(q)-float64(p))*t)) }
	return color.NRGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

var (
	white = color.NRGBA{255, 255, 255, 255}
	black = color.NRGBA{0, 0, 0, 255}
)

func (icon *svgIcon) accent() color.NRGBA {
	best, score := color.NRGBA{0x33, 0x41, 0x55, 255}, 0.0
	try := func(c color.NRGBA) {
		chroma := float64(max(c.R, c.G, c.B)-min(c.R, c.G, c.B)) / 255
		if s := chroma * min(1, contrast(c, white)/4.5); s > score {
			best, score = color.NRGBA{c.R, c.G, c.B, 255}, s
		}
	}
	for _, sh := range icon.shapes {
		for _, p := range []paint{sh.st.fill, sh.st.stroke} {
			switch {
			case p.none:
			case p.g != nil:
				for _, s := range p.g.stops {
					try(s.c)
				}
			default:
				try(p.c)
			}
		}
	}
	return best
}

func boxBlur(a []float32, w, h, r int) {
	tmp := make([]float32, max(w, h))
	pass := func(n, step, stride, lines int) {
		for l := range lines {
			base := l * stride
			var sum float32
			for i := -r; i <= r; i++ {
				sum += a[base+min(max(i, 0), n-1)*step]
			}
			for i := range n {
				tmp[i] = sum / float32(2*r+1)
				sum += a[base+min(i+r+1, n-1)*step] - a[base+max(i-r, 0)*step]
			}
			for i := range n {
				a[base+i*step] = tmp[i]
			}
		}
	}
	for range 3 {
		pass(w, 1, w, h)
		pass(h, w, 1, w)
	}
}

func prepareOGImage(t ogText, favicon []byte) (func() []byte, error) {
	icon, err := parseIcon(favicon)
	if err != nil {
		return nil, err
	}
	semi, bold := faces()[1], faces()[2]
	for _, c := range []struct {
		fc   *face
		s, k string
	}{{bold, t.host, "site.yml: baseURL:"}, {bold, t.title, "site.yml: title:"}, {semi, t.tagline, t.from + ":"}} {
		if err := c.fc.check(c.s); err != nil {
			return nil, fmt.Errorf("%s %v", c.k, err)
		}
	}
	return func() []byte { return drawOGImage(t, icon) }, nil
}

func drawOGImage(t ogText, icon *svgIcon) []byte {
	icons := make(chan *image.NRGBA, 1)
	go func() { icons <- icon.render(ogIcon, false) }()
	semi, bold := faces()[1], faces()[2]
	raw := icon.accent()
	bg := mix(white, raw, ogTint)
	accent := raw
	for contrast(accent, bg) < 4.5 {
		accent = mix(accent, black, 0.08)
	}
	ink := mix(black, accent, ogInkAccent)

	img := image.NewRGBA(image.Rect(0, 0, ogW, ogH))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, ogH-ogBar, ogW, ogH), image.NewUniform(raw), image.Point{}, draw.Src)
	bold.draw(img, t.host, ogMargin, ogHostBase, ogHostPx, accent)
	titlePx := float64(ogTitlePx)
	if w := bold.measure(t.title, titlePx); w > ogCol {
		titlePx = max(ogTitleMin, math.Floor(titlePx*ogCol/w))
	}
	title := wrapLines(t.title, 1, func(s string) bool { return bold.measure(s, titlePx) <= ogCol })
	if len(title) > 0 {
		bold.draw(img, title[0], ogMargin, ogTitleBase, titlePx, ink)
	}
	tags := wrapLines(t.tagline, ogTagLines, func(s string) bool { return semi.measure(s, ogTagPx) <= ogCol })
	for i, s := range tags {
		semi.draw(img, s, ogMargin, float64(ogTagBase+i*ogTagStep), ogTagPx, ink)
	}

	ic := <-icons
	ix, iy := ogIconX, (ogH-ogBar-ogIcon)/2
	pad := 4 * ogShadow
	sw := ogIcon + 2*pad
	alpha := make([]float32, sw*sw)
	for y := range ogIcon {
		for x := range ogIcon {
			alpha[(y+pad)*sw+x+pad] = float32(ic.Pix[ic.PixOffset(x, y)+3])
		}
	}
	boxBlur(alpha, sw, sw, ogShadow)
	mask := image.NewAlpha(image.Rect(0, 0, sw, sw))
	for i, a := range alpha {
		mask.Pix[i] = uint8(min(255, math.Round(float64(a))))
	}
	shade := mix(black, accent, 0.35)
	shade.A = 70
	draw.DrawMask(img, image.Rect(ix-pad, iy-pad+ogShadowDY, ix-pad+sw, iy-pad+ogShadowDY+sw), image.NewUniform(shade), image.Point{}, mask, image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(ix, iy, ix+ogIcon, iy+ogIcon), ic, image.Point{}, draw.Over)
	return encodeRGB(img)
}

func encodeRGB(img *image.RGBA) []byte {
	const strips = 8
	w, h := img.Rect.Dx(), img.Rect.Dy()
	n := 1 + 3*w
	raw := make([]byte, h*n)
	parts := make([][]byte, strips)
	var wg sync.WaitGroup
	for s := range strips {
		wg.Go(func() {
			y0, y1 := s*h/strips, (s+1)*h/strips
			cur, prev := make([]byte, 3*w), make([]byte, 3*w)
			if y0 > 0 {
				rgbRow(prev, img, y0-1)
			}
			for y := y0; y < y1; y++ {
				rgbRow(cur, img, y)
				row := raw[y*n : (y+1)*n]
				row[0] = 2
				for i, c := range cur {
					row[i+1] = c - prev[i]
				}
				cur, prev = prev, cur
			}
			var b bytes.Buffer
			fw, _ := flate.NewWriter(&b, flate.DefaultCompression)
			fw.Write(raw[y0*n : y1*n])
			if s == strips-1 {
				fw.Close()
			} else {
				fw.Flush()
			}
			parts[s] = b.Bytes()
		})
	}
	wg.Wait()
	z := []byte{0x78, 0x9c}
	for _, p := range parts {
		z = append(z, p...)
	}
	z = binary.BigEndian.AppendUint32(z, adler32.Checksum(raw))
	out := []byte("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
		start := len(out)
		out = append(append(out, typ...), data...)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[start:]))
	}
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, uint32(w)), uint32(h))
	chunk("IHDR", append(ihdr, 8, 2, 0, 0, 0))
	chunk("IDAT", z)
	chunk("IEND", nil)
	return out
}

func rgbRow(dst []byte, img *image.RGBA, y int) {
	src := img.Pix[y*img.Stride:]
	for x := range len(dst) / 3 {
		dst[3*x], dst[3*x+1], dst[3*x+2] = src[4*x], src[4*x+1], src[4*x+2]
	}
}

func (p *Project) planOGImage(old, ns *State, files map[string]bool) []writeJob {
	if slices.Contains(p.static, ogName) {
		p.errs.add("static/%s: lyn renders it from the site title, the home page summary and static/favicon.svg; delete this file", ogName)
		return nil
	}
	if !slices.Contains(p.static, "favicon.svg") {
		return nil
	}
	favicon, err := os.ReadFile(filepath.Join(p.Root, "static", "favicon.svg"))
	if err != nil {
		p.errs.add("static/favicon.svg: %v", err)
		return nil
	}
	t := p.ogText()
	stamp := "og:" + strconv.FormatUint(xxh3.Hash(bytes.Join([][]byte{[]byte(t.host), []byte(t.title), []byte(t.tagline), favicon}, []byte{0})), 36)
	files[ogName] = true
	ns.Files[ogName] = stamp
	if old != nil && old.Files[ogName] == stamp {
		return nil
	}
	gen, err := prepareOGImage(t, favicon)
	if err != nil {
		if _, perr := parseIcon(favicon); perr == nil {
			p.errs.add("%v", err)
		}
		delete(files, ogName)
		delete(ns.Files, ogName)
		return nil
	}
	input := bytes.Join([][]byte{[]byte(t.host), []byte(t.title), []byte(t.tagline), favicon}, []byte{0})
	return []writeJob{{dst: ogName, gen: func() []byte { return renderCached("og", input, gen) }}}
}
