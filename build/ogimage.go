package build

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

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
	ogIcon      = 300
	ogGap       = 64
	ogBar       = 16
	ogHostPx    = 26
	ogTitlePx   = 76
	ogTitleMin  = 56
	ogTagPx     = 36
	ogTagLines  = 4
	ogShadow    = 14
	ogShadowDY  = 16
	ogTint      = 0.07
	ogInkAccent = 0.14
)

var plainMD = goldmark.New(goldmark.WithExtensions(extension.Typographer))

func inlinePlain(s string) string {
	src := []byte(inlineOnly(s))
	return strings.Join(strings.Fields(plainText(plainMD.Parser().Parse(text.NewReader(src)), src)), " ")
}

type ogText struct {
	host, title, tagline, from string
}

func (p *Project) ogText() ogText {
	t := ogText{title: strings.Join(strings.Fields(p.Title), " "), from: "site.yml"}
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
	regular, bold := fontFace(false), fontFace(true)
	for _, c := range []struct {
		fc   *face
		s, k string
	}{{bold, t.host, "site.yml: baseURL:"}, {bold, t.title, "site.yml: title:"}, {regular, t.tagline, t.from + ":"}} {
		if err := c.fc.check(c.s); err != nil {
			return nil, fmt.Errorf("%s %v", c.k, err)
		}
	}
	return func() []byte { return drawOGImage(t, icon) }, nil
}

func drawOGImage(t ogText, icon *svgIcon) []byte {
	regular, bold := fontFace(false), fontFace(true)
	accent := icon.accent()
	bg := mix(white, accent, ogTint)
	for contrast(accent, bg) < 4.5 {
		accent = mix(accent, black, 0.08)
	}
	ink := mix(black, accent, ogInkAccent)
	soft := mix(ink, bg, 0.18)

	col := float64(ogW - 2*ogMargin - ogIcon - ogGap)
	titlePx := float64(ogTitlePx)
	if w := bold.measure(t.title, titlePx); w > col {
		titlePx = max(ogTitleMin, math.Floor(titlePx*col/w))
	}
	titles := wrapLines(t.title, 2, func(s string) bool { return bold.measure(s, titlePx) <= col })
	tags := wrapLines(t.tagline, ogTagLines, func(s string) bool { return regular.measure(s, ogTagPx) <= col })

	type line struct {
		fc     *face
		s      string
		px, lh float64
		c      color.NRGBA
		gap    float64
	}
	lines := []line{{bold, t.host, ogHostPx, ogHostPx * 1.3, accent, 0}}
	for i, s := range titles {
		gap := 0.0
		if i == 0 {
			gap = 18
		}
		lines = append(lines, line{bold, s, titlePx, titlePx * 1.12, ink, gap})
	}
	for i, s := range tags {
		gap := 0.0
		if i == 0 {
			gap = 26
		}
		lines = append(lines, line{regular, s, ogTagPx, ogTagPx * 1.36, soft, gap})
	}
	var height float64
	for _, l := range lines {
		height += l.gap + l.lh
	}
	img := image.NewRGBA(image.Rect(0, 0, ogW, ogH))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, ogH-ogBar, ogW, ogH), image.NewUniform(accent), image.Point{}, draw.Src)
	y := math.Round((ogH - ogBar - height) / 2)
	for _, l := range lines {
		y += l.gap
		base := y + (l.lh-(faceAscent+faceDescent)*l.px)/2 + faceAscent*l.px
		l.fc.draw(img, l.s, ogMargin, math.Round(base), l.px, l.c)
		y += l.lh
	}

	ic := icon.render(ogIcon, false)
	ix, iy := ogW-ogMargin-ogIcon, (ogH-ogBar-ogIcon)/2
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
	var b bytes.Buffer
	(&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&b, &image.NRGBA{Pix: img.Pix, Stride: img.Stride, Rect: img.Rect})
	return b.Bytes()
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
	return []writeJob{{dst: ogName, gen: gen}}
}
