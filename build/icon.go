package build

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/image/vector"
)

const (
	touchIconSize = 180
	faviconSize   = 32
)

type pt struct{ x, y float64 }

type affine [6]float64

var identity = affine{1, 0, 0, 1, 0, 0}

func (m affine) apply(p pt) pt {
	return pt{m[0]*p.x + m[2]*p.y + m[4], m[1]*p.x + m[3]*p.y + m[5]}
}

func (m affine) mul(n affine) affine {
	return affine{
		m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

func (m affine) scale() float64 { return math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2])) }

type gradStop struct {
	off float64
	c   color.NRGBA
}

type gradient struct {
	x1, y1, x2, y2 float64
	user           bool
	stops          []gradStop
}

type paint struct {
	none bool
	c    color.NRGBA
	ref  string
	g    *gradient
}

type style struct {
	fill, stroke paint
	width        float64
	cap, join    string
}

type iconShape struct {
	subs [][]pt
	shut []bool
	st   style
	m    affine
	bbox [4]float64
}

type svgIcon struct {
	vb     [4]float64
	shapes []*iconShape
}

func parseColor(s string) (color.NRGBA, error) {
	switch s {
	case "white":
		return color.NRGBA{255, 255, 255, 255}, nil
	case "black":
		return color.NRGBA{0, 0, 0, 255}, nil
	}
	if !strings.HasPrefix(s, "#") || (len(s) != 4 && len(s) != 7) {
		return color.NRGBA{}, fmt.Errorf("color %q: use #rgb or #rrggbb", s)
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("color %q: use #rgb or #rrggbb", s)
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}

func parseIcon(src []byte) (*svgIcon, error) {
	dec := xml.NewDecoder(bytes.NewReader(src))
	icon := &svgIcon{}
	grads := map[string]*gradient{}
	type frame struct {
		st style
		m  affine
	}
	stack := []frame{{style{fill: paint{c: color.NRGBA{0, 0, 0, 255}}, stroke: paint{none: true}, width: 1, cap: "butt", join: "miter"}, identity}}
	var cur *gradient
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			switch t.Name.Local {
			case "svg", "g":
				stack = stack[:len(stack)-1]
			case "linearGradient":
				cur = nil
			}
		case xml.StartElement:
			attr := map[string]string{}
			for _, a := range t.Attr {
				if a.Name.Space == "" || a.Name.Space == "http://www.w3.org/2000/svg" {
					attr[a.Name.Local] = a.Value
				}
			}
			top := stack[len(stack)-1]
			st, m := top.st, top.m
			for k, v := range attr {
				switch k {
				case "fill", "stroke":
					p := paint{}
					switch {
					case v == "none":
						p.none = true
					case strings.HasPrefix(v, "url(#") && strings.HasSuffix(v, ")"):
						p.ref = v[5 : len(v)-1]
					default:
						c, err := parseColor(v)
						if err != nil {
							return nil, fmt.Errorf("<%s %s>: %v", t.Name.Local, k, err)
						}
						p.c = c
					}
					if k == "fill" {
						st.fill = p
					} else {
						st.stroke = p
					}
				case "stroke-width":
					w, err := strconv.ParseFloat(v, 64)
					if err != nil {
						return nil, fmt.Errorf("stroke-width %q is not a number", v)
					}
					st.width = w
				case "stroke-linecap":
					if v != "butt" && v != "round" && v != "square" {
						return nil, fmt.Errorf("stroke-linecap %q: use butt, round or square", v)
					}
					st.cap = v
				case "stroke-linejoin":
					if v != "miter" && v != "round" && v != "bevel" {
						return nil, fmt.Errorf("stroke-linejoin %q: use miter, round or bevel", v)
					}
					st.join = v
				case "transform":
					tm, err := parseTransform(v)
					if err != nil {
						return nil, err
					}
					m = m.mul(tm)
				case "xmlns", "viewBox", "width", "height", "x", "y", "rx", "ry", "cx", "cy", "r", "d", "id",
					"x1", "y1", "x2", "y2", "gradientUnits", "offset", "stop-color", "points":
				default:
					return nil, fmt.Errorf("<%s>: attribute %s is not supported in icons", t.Name.Local, k)
				}
			}
			num := func(k string) float64 {
				f, _ := strconv.ParseFloat(strings.TrimSuffix(attr[k], "px"), 64)
				return f
			}
			var sh *iconShape
			switch t.Name.Local {
			case "svg":
				f := strings.Fields(strings.ReplaceAll(attr["viewBox"], ",", " "))
				if len(f) != 4 {
					return nil, fmt.Errorf("<svg> needs a viewBox")
				}
				for i := range 4 {
					icon.vb[i], _ = strconv.ParseFloat(f[i], 64)
				}
				if icon.vb[2] != icon.vb[3] || icon.vb[2] <= 0 {
					return nil, fmt.Errorf("viewBox %q must be a square", attr["viewBox"])
				}
				stack = append(stack, frame{st, m})
			case "g":
				stack = append(stack, frame{st, m})
			case "defs", "stop":
				if t.Name.Local == "stop" && cur != nil {
					c, err := parseColor(attr["stop-color"])
					if err != nil {
						return nil, fmt.Errorf("<stop>: %v", err)
					}
					off := num("offset")
					if before, ok := strings.CutSuffix(attr["offset"], "%"); ok {
						f, _ := strconv.ParseFloat(before, 64)
						off = f / 100
					}
					cur.stops = append(cur.stops, gradStop{off, c})
				}
			case "linearGradient":
				cur = &gradient{x1: 0, y1: 0, x2: 1, y2: 0, user: attr["gradientUnits"] == "userSpaceOnUse"}
				for _, k := range []string{"x1", "y1", "x2", "y2"} {
					if v, ok := attr[k]; ok {
						f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
						if err != nil {
							return nil, fmt.Errorf("<linearGradient %s=%q> is not a number", k, v)
						}
						if strings.HasSuffix(v, "%") {
							f /= 100
						}
						switch k {
						case "x1":
							cur.x1 = f
						case "y1":
							cur.y1 = f
						case "x2":
							cur.x2 = f
						case "y2":
							cur.y2 = f
						}
					}
				}
				if attr["id"] == "" {
					return nil, fmt.Errorf("<linearGradient> needs an id")
				}
				grads[attr["id"]] = cur
			case "rect":
				x, y, w, h := num("x"), num("y"), num("width"), num("height")
				rx, ry := num("rx"), num("ry")
				if _, ok := attr["ry"]; !ok {
					ry = rx
				}
				if _, ok := attr["rx"]; !ok {
					rx = ry
				}
				sh = &iconShape{}
				sh.subs, sh.shut = [][]pt{roundRect(x, y, w, h, min(rx, w/2), min(ry, h/2))}, []bool{true}
			case "circle":
				sh = &iconShape{}
				sh.subs, sh.shut = [][]pt{ellipse(num("cx"), num("cy"), num("r"), num("r"))}, []bool{true}
			case "path":
				subs, shut, err := parsePath(attr["d"])
				if err != nil {
					return nil, fmt.Errorf("<path d>: %v", err)
				}
				sh = &iconShape{subs: subs, shut: shut}
			default:
				return nil, fmt.Errorf("<%s> is not supported in icons; use rect, circle, path, g and linearGradient", t.Name.Local)
			}
			if sh != nil {
				sh.st, sh.m = st, m
				icon.shapes = append(icon.shapes, sh)
			}
		}
	}
	if icon.vb[2] == 0 {
		return nil, fmt.Errorf("missing <svg> root")
	}
	if len(icon.shapes) == 0 {
		return nil, fmt.Errorf("the icon draws nothing")
	}
	for _, sh := range icon.shapes {
		for _, p := range []*paint{&sh.st.fill, &sh.st.stroke} {
			if p.ref == "" {
				continue
			}
			g := grads[p.ref]
			if g == nil || len(g.stops) == 0 {
				return nil, fmt.Errorf("gradient %q is not defined or has no stops", p.ref)
			}
			p.g = g
		}
		sh.bbox = bounds(sh.subs)
	}
	return icon, nil
}

func bounds(subs [][]pt) [4]float64 {
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, s := range subs {
		for _, p := range s {
			b[0], b[1] = min(b[0], p.x), min(b[1], p.y)
			b[2], b[3] = max(b[2], p.x), max(b[3], p.y)
		}
	}
	return b
}

func parseTransform(v string) (affine, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "matrix(") || !strings.HasSuffix(v, ")") {
		return identity, fmt.Errorf("transform %q: only matrix(a b c d e f) is supported", v)
	}
	f := strings.Fields(strings.ReplaceAll(v[7:len(v)-1], ",", " "))
	if len(f) != 6 {
		return identity, fmt.Errorf("transform %q needs six numbers", v)
	}
	var m affine
	for i := range 6 {
		x, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return identity, fmt.Errorf("transform %q: %v", v, err)
		}
		m[i] = x
	}
	return m, nil
}

func ellipse(cx, cy, rx, ry float64) []pt {
	const n = 96
	out := make([]pt, 0, n)
	for i := range n {
		a := 2 * math.Pi * float64(i) / n
		out = append(out, pt{cx + rx*math.Cos(a), cy + ry*math.Sin(a)})
	}
	return out
}

func roundRect(x, y, w, h, rx, ry float64) []pt {
	if rx <= 0 || ry <= 0 {
		return []pt{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
	}
	var out []pt
	corner := func(cx, cy, a0 float64) {
		for i := 0; i <= 16; i++ {
			a := a0 + math.Pi/2*float64(i)/16
			out = append(out, pt{cx + rx*math.Cos(a), cy + ry*math.Sin(a)})
		}
	}
	corner(x+w-rx, y+ry, -math.Pi/2)
	corner(x+w-rx, y+h-ry, 0)
	corner(x+rx, y+h-ry, math.Pi/2)
	corner(x+rx, y+ry, math.Pi)
	return out
}

func parsePath(d string) ([][]pt, []bool, error) {
	var nums []float64
	var subs [][]pt
	var shut []bool
	var cur []pt
	var p, start, ctrl pt
	var prev byte
	flush := func(closed bool) {
		if len(cur) > 1 {
			subs = append(subs, cur)
			shut = append(shut, closed)
		}
		cur = nil
	}
	i := 0
	readNum := func() (float64, bool) {
		for i < len(d) && (d[i] == ' ' || d[i] == ',' || d[i] == '\n' || d[i] == '\t' || d[i] == '\r') {
			i++
		}
		j := i
		if j < len(d) && (d[j] == '-' || d[j] == '+') {
			j++
		}
		dot := false
		for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !dot) {
			if d[j] == '.' {
				dot = true
			}
			j++
		}
		if j < len(d) && (d[j] == 'e' || d[j] == 'E') {
			j++
			if j < len(d) && (d[j] == '-' || d[j] == '+') {
				j++
			}
			for j < len(d) && d[j] >= '0' && d[j] <= '9' {
				j++
			}
		}
		if j == i {
			return 0, false
		}
		f, err := strconv.ParseFloat(d[i:j], 64)
		if err != nil {
			return 0, false
		}
		i = j
		return f, true
	}
	readFlag := func() (float64, bool) {
		for i < len(d) && (d[i] == ' ' || d[i] == ',') {
			i++
		}
		if i < len(d) && (d[i] == '0' || d[i] == '1') {
			i++
			return float64(d[i-1] - '0'), true
		}
		return 0, false
	}
	cubic := func(c1, c2, e pt) {
		for k := 1; k <= 16; k++ {
			t := float64(k) / 16
			u := 1 - t
			cur = append(cur, pt{u*u*u*p.x + 3*u*u*t*c1.x + 3*u*t*t*c2.x + t*t*t*e.x, u*u*u*p.y + 3*u*u*t*c1.y + 3*u*t*t*c2.y + t*t*t*e.y})
		}
		ctrl, p = c2, e
	}
	quad := func(c, e pt) {
		for k := 1; k <= 16; k++ {
			t := float64(k) / 16
			u := 1 - t
			cur = append(cur, pt{u*u*p.x + 2*u*t*c.x + t*t*e.x, u*u*p.y + 2*u*t*c.y + t*t*e.y})
		}
		ctrl, p = c, e
	}
	for {
		for i < len(d) && (d[i] == ' ' || d[i] == ',' || d[i] == '\n' || d[i] == '\t' || d[i] == '\r') {
			i++
		}
		if i >= len(d) {
			break
		}
		cmd := d[i]
		if strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", cmd) >= 0 {
			i++
		} else if prev != 0 && prev != 'Z' && prev != 'z' {
			cmd = prev
			if cmd == 'M' {
				cmd = 'L'
			} else if cmd == 'm' {
				cmd = 'l'
			}
		} else {
			return nil, nil, fmt.Errorf("unexpected %q", d[i])
		}
		rel := cmd >= 'a'
		off := func(q pt) pt {
			if rel {
				return pt{p.x + q.x, p.y + q.y}
			}
			return q
		}
		need := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2, 'A': 7, 'Z': 0}[cmd&^0x20]
		nums = nums[:0]
		for k := range need {
			var f float64
			var ok bool
			if cmd&^0x20 == 'A' && (k == 3 || k == 4) {
				f, ok = readFlag()
			} else {
				f, ok = readNum()
			}
			if !ok {
				return nil, nil, fmt.Errorf("command %c needs %d numbers", cmd, need)
			}
			nums = append(nums, f)
		}
		up := cmd &^ 0x20
		switch up {
		case 'M':
			flush(false)
			p = off(pt{nums[0], nums[1]})
			start = p
			cur = []pt{p}
		case 'L':
			p = off(pt{nums[0], nums[1]})
			cur = append(cur, p)
		case 'H':
			if rel {
				p.x += nums[0]
			} else {
				p.x = nums[0]
			}
			cur = append(cur, p)
		case 'V':
			if rel {
				p.y += nums[0]
			} else {
				p.y = nums[0]
			}
			cur = append(cur, p)
		case 'C':
			cubic(off(pt{nums[0], nums[1]}), off(pt{nums[2], nums[3]}), off(pt{nums[4], nums[5]}))
		case 'S':
			c1 := p
			if pu := prev &^ 0x20; pu == 'C' || pu == 'S' {
				c1 = pt{2*p.x - ctrl.x, 2*p.y - ctrl.y}
			}
			cubic(c1, off(pt{nums[0], nums[1]}), off(pt{nums[2], nums[3]}))
		case 'Q':
			quad(off(pt{nums[0], nums[1]}), off(pt{nums[2], nums[3]}))
		case 'T':
			c := p
			if pu := prev &^ 0x20; pu == 'Q' || pu == 'T' {
				c = pt{2*p.x - ctrl.x, 2*p.y - ctrl.y}
			}
			quad(c, off(pt{nums[0], nums[1]}))
		case 'A':
			e := off(pt{nums[5], nums[6]})
			cur = append(cur, arc(p, e, nums[0], nums[1], nums[2], nums[3] != 0, nums[4] != 0)...)
			p = e
		case 'Z':
			if len(cur) > 0 {
				flush(true)
			}
			p = start
			cur = []pt{p}
		}
		if len(cur) == 0 {
			cur = []pt{p}
		}
		prev = cmd
		if up == 'M' {
			if rel {
				prev = 'l'
			} else {
				prev = 'L'
			}
		}
	}
	flush(false)
	return subs, shut, nil
}

func arc(p0, p1 pt, rx, ry, phi float64, large, sweep bool) []pt {
	if rx == 0 || ry == 0 || p0 == p1 {
		return []pt{p1}
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	s, c := math.Sincos(phi * math.Pi / 180)
	dx, dy := (p0.x-p1.x)/2, (p0.y-p1.y)/2
	x1, y1 := c*dx+s*dy, -s*dx+c*dy
	if l := x1*x1/(rx*rx) + y1*y1/(ry*ry); l > 1 {
		rx, ry = rx*math.Sqrt(l), ry*math.Sqrt(l)
	}
	num := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	den := rx*rx*y1*y1 + ry*ry*x1*x1
	k := math.Sqrt(math.Max(0, num/den))
	if large == sweep {
		k = -k
	}
	cx1, cy1 := k*rx*y1/ry, -k*ry*x1/rx
	cx, cy := c*cx1-s*cy1+(p0.x+p1.x)/2, s*cx1+c*cy1+(p0.y+p1.y)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := ang(1, 0, (x1-cx1)/rx, (y1-cy1)/ry)
	dt := ang((x1-cx1)/rx, (y1-cy1)/ry, (-x1-cx1)/rx, (-y1-cy1)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	n := max(4, int(math.Ceil(math.Abs(dt)/(math.Pi/32))))
	out := make([]pt, 0, n)
	for i := 1; i <= n; i++ {
		t := t1 + dt*float64(i)/float64(n)
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		out = append(out, pt{c*ex - s*ey + cx, s*ex + c*ey + cy})
	}
	out[len(out)-1] = p1
	return out
}

func area(poly []pt) float64 {
	var a float64
	for i := range poly {
		j := (i + 1) % len(poly)
		a += poly[i].x*poly[j].y - poly[j].x*poly[i].y
	}
	return a
}

func strokePolys(sub []pt, closed bool, w float64, capStyle, join string) [][]pt {
	pts := sub[:0:0]
	for _, q := range sub {
		if len(pts) == 0 || math.Hypot(q.x-pts[len(pts)-1].x, q.y-pts[len(pts)-1].y) > 1e-9 {
			pts = append(pts, q)
		}
	}
	if closed && len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	hw := w / 2
	var out [][]pt
	disc := func(c pt) { out = append(out, ellipse(c.x, c.y, hw, hw)) }
	if len(pts) == 1 {
		if capStyle == "round" {
			disc(pts[0])
		}
		return out
	}
	n := len(pts)
	segs := n - 1
	if closed {
		segs = n
	}
	norm := func(a, b pt) (pt, pt) {
		dx, dy := b.x-a.x, b.y-a.y
		l := math.Hypot(dx, dy)
		return pt{dx / l, dy / l}, pt{-dy / l * hw, dx / l * hw}
	}
	for i := range segs {
		a, b := pts[i], pts[(i+1)%n]
		d, o := norm(a, b)
		if !closed && capStyle == "square" {
			if i == 0 {
				a = pt{a.x - d.x*hw, a.y - d.y*hw}
			}
			if i == segs-1 {
				b = pt{b.x + d.x*hw, b.y + d.y*hw}
			}
		}
		out = append(out, []pt{{a.x + o.x, a.y + o.y}, {b.x + o.x, b.y + o.y}, {b.x - o.x, b.y - o.y}, {a.x - o.x, a.y - o.y}})
	}
	for i := range n {
		if !closed && (i == 0 || i == n-1) {
			if capStyle == "round" {
				disc(pts[i])
			}
			continue
		}
		prev, cur, next := pts[(i-1+n)%n], pts[i], pts[(i+1)%n]
		d1, o1 := norm(prev, cur)
		d2, o2 := norm(cur, next)
		cross := d1.x*d2.y - d1.y*d2.x
		if math.Abs(cross) < 1e-9 && d1.x*d2.x+d1.y*d2.y > 0 {
			continue
		}
		if join == "round" {
			disc(cur)
			continue
		}
		if cross > 0 {
			o1, o2 = pt{-o1.x, -o1.y}, pt{-o2.x, -o2.y}
		}
		a, b := pt{cur.x + o1.x, cur.y + o1.y}, pt{cur.x + o2.x, cur.y + o2.y}
		if join == "miter" {
			cosHalf := math.Sqrt(math.Max(0, (1+d1.x*d2.x+d1.y*d2.y)/2))
			if cosHalf > 1e-9 && 1/cosHalf <= 4 {
				mx, my := o1.x+o2.x, o1.y+o2.y
				ml := math.Hypot(mx, my)
				tip := pt{cur.x + mx/ml*hw/cosHalf, cur.y + my/ml*hw/cosHalf}
				out = append(out, []pt{cur, a, tip, b})
				continue
			}
		}
		out = append(out, []pt{cur, a, b})
	}
	return out
}

type gradImage struct {
	g      *gradient
	x0, y0 float64
	dx, dy float64
	ll     float64
}

func (g *gradImage) ColorModel() color.Model { return color.NRGBAModel }
func (g *gradImage) Bounds() image.Rectangle {
	return image.Rect(-1e6, -1e6, 1e6, 1e6)
}
func (g *gradImage) At(x, y int) color.Color {
	t := ((float64(x)+0.5-g.x0)*g.dx + (float64(y)+0.5-g.y0)*g.dy) / g.ll
	s := g.g.stops
	if t <= s[0].off {
		return s[0].c
	}
	for i := 1; i < len(s); i++ {
		if t <= s[i].off {
			f := (t - s[i-1].off) / (s[i].off - s[i-1].off)
			a, b := s[i-1].c, s[i].c
			mix := func(p, q uint8) uint8 { return uint8(math.Round(float64(p) + (float64(q)-float64(p))*f)) }
			return color.NRGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), mix(a.A, b.A)}
		}
	}
	return s[len(s)-1].c
}

func paintSource(p paint, sh *iconShape, dev affine) image.Image {
	if p.g == nil {
		return image.NewUniform(p.c)
	}
	g := p.g
	a, b := pt{g.x1, g.y1}, pt{g.x2, g.y2}
	if !g.user {
		bb := sh.bbox
		a = pt{bb[0] + a.x*(bb[2]-bb[0]), bb[1] + a.y*(bb[3]-bb[1])}
		b = pt{bb[0] + b.x*(bb[2]-bb[0]), bb[1] + b.y*(bb[3]-bb[1])}
	}
	m := dev.mul(sh.m)
	a, b = m.apply(a), m.apply(b)
	dx, dy := b.x-a.x, b.y-a.y
	return &gradImage{g: g, x0: a.x, y0: a.y, dx: dx, dy: dy, ll: math.Max(dx*dx+dy*dy, 1e-9)}
}

func fillPolys(dst *image.NRGBA, polys [][]pt, m affine, src image.Image) {
	var dev [][]pt
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, poly := range polys {
		if len(poly) < 3 {
			continue
		}
		q := make([]pt, len(poly))
		for i, p := range poly {
			q[i] = m.apply(p)
			b[0], b[1] = min(b[0], q[i].x), min(b[1], q[i].y)
			b[2], b[3] = max(b[2], q[i].x), max(b[3], q[i].y)
		}
		dev = append(dev, q)
	}
	r := image.Rect(int(math.Floor(b[0])), int(math.Floor(b[1])), int(math.Ceil(b[2])), int(math.Ceil(b[3]))).Intersect(dst.Bounds())
	if len(dev) == 0 || r.Empty() {
		return
	}
	z := vector.NewRasterizer(r.Dx(), r.Dy())
	ox, oy := float64(r.Min.X), float64(r.Min.Y)
	for _, q := range dev {
		z.MoveTo(float32(q[0].x-ox), float32(q[0].y-oy))
		for _, p := range q[1:] {
			z.LineTo(float32(p.x-ox), float32(p.y-oy))
		}
		z.ClosePath()
	}
	z.Draw(dst, r, src, r.Min)
}

func (icon *svgIcon) render(size int, opaque bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	k := float64(size) / icon.vb[2]
	dev := affine{k, 0, 0, k, -icon.vb[0] * k, -icon.vb[1] * k}
	if opaque {
		bg := icon.shapes[0]
		full := &iconShape{subs: [][]pt{{{icon.vb[0], icon.vb[1]}, {icon.vb[0] + icon.vb[2], icon.vb[1]}, {icon.vb[0] + icon.vb[2], icon.vb[1] + icon.vb[3]}, {icon.vb[0], icon.vb[1] + icon.vb[3]}}}, m: identity, bbox: bg.bbox}
		p := bg.st.stroke
		if p.none || bg.st.width <= 0 {
			p = bg.st.fill
		}
		if p.none {
			p = paint{c: color.NRGBA{255, 255, 255, 255}}
		}
		fillPolys(img, full.subs, dev, paintSource(p, &iconShape{m: bg.m, bbox: bg.bbox}, dev))
	}
	for _, sh := range icon.shapes {
		m := dev.mul(sh.m)
		if !sh.st.fill.none {
			fillPolys(img, sh.subs, m, paintSource(sh.st.fill, sh, dev))
		}
		if !sh.st.stroke.none && sh.st.width > 0 {
			var polys [][]pt
			for i, sub := range sh.subs {
				for _, poly := range strokePolys(sub, sh.shut[i], sh.st.width, sh.st.cap, sh.st.join) {
					if area(poly) < 0 {
						for a, b := 0, len(poly)-1; a < b; a, b = a+1, b-1 {
							poly[a], poly[b] = poly[b], poly[a]
						}
					}
					polys = append(polys, poly)
				}
			}
			fillPolys(img, polys, m, paintSource(sh.st.stroke, sh, dev))
		}
	}
	return img
}

func encodePNG(img *image.NRGBA) []byte {
	var out image.Image = img
	seen := map[color.NRGBA]uint8{}
	var pal color.Palette
	for i := 0; i < len(img.Pix) && len(pal) <= 256; i += 4 {
		c := color.NRGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
		if _, ok := seen[c]; !ok {
			seen[c] = uint8(len(pal))
			pal = append(pal, c)
		}
	}
	if len(pal) <= 256 {
		p := image.NewPaletted(img.Bounds(), pal)
		for i := 0; i < len(img.Pix); i += 4 {
			p.Pix[i/4] = seen[color.NRGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}]
		}
		out = p
	}
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	enc.Encode(&b, out)
	return b.Bytes()
}

func encodeICO(img *image.NRGBA) []byte {
	data := encodePNG(img)
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, 1})
	s := uint8(img.Bounds().Dx())
	b.Write([]byte{s, s, 0, 0})
	binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
	binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(data)), 22})
	b.Write(data)
	return b.Bytes()
}

func renderIcons(src []byte) (touch, ico []byte, err error) {
	icon, err := parseIcon(src)
	if err != nil {
		return nil, nil, err
	}
	t := icon.render(touchIconSize, true)
	for i := 3; i < len(t.Pix); i += 4 {
		if t.Pix[i] != 255 {
			return nil, nil, fmt.Errorf("the touch icon is not opaque; start the icon with a shape that has a solid fill")
		}
	}
	return encodePNG(t), encodeICO(icon.render(faviconSize, false)), nil
}

var generatedIcons = []string{"apple-touch-icon.png", "favicon.ico"}

func (p *Project) planIcons(old, ns *State, files map[string]bool) []writeJob {
	src := -1
	for i, f := range p.static {
		switch {
		case f == "favicon.svg":
			src = i
		case slices.Contains(generatedIcons, f):
			p.errs.add("static/%s: lyn renders it from static/favicon.svg; delete this file", f)
		}
	}
	if src < 0 {
		p.errs.add("static/favicon.svg is required; lyn renders apple-touch-icon.png and favicon.ico from it")
		return nil
	}
	stamp := "icon:" + p.staticStamps[src]
	cached := old != nil && p.staticStamps[src] != ""
	for _, name := range generatedIcons {
		cached = cached && old.Files[name] == stamp
	}
	if cached {
		for _, name := range generatedIcons {
			files[name] = true
			ns.Files[name] = stamp
		}
		return nil
	}
	b, err := os.ReadFile(filepath.Join(p.Root, "static", "favicon.svg"))
	if err == nil {
		var touch, ico []byte
		var both []byte
		both = renderCached("icons", b, func() []byte {
			touch, ico, rerr := renderIcons(b)
			if rerr != nil {
				err = rerr
				return nil
			}
			return append(binary.LittleEndian.AppendUint32(nil, uint32(len(touch))), append(touch, ico...)...)
		})
		if err == nil && len(both) > 4 {
			n := binary.LittleEndian.Uint32(both)
			touch, ico = both[4:4+n], both[4+n:]
			var jobs []writeJob
			for i, data := range [][]byte{touch, ico} {
				files[generatedIcons[i]] = true
				ns.Files[generatedIcons[i]] = stamp
				jobs = append(jobs, writeJob{dst: generatedIcons[i], data: data})
			}
			return jobs
		}
	}
	p.errs.add("static/favicon.svg: %v", err)
	return nil
}
