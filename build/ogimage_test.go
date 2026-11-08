package build

import (
	"bytes"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOGImage(t *testing.T) {
	d := site(t)
	b := NewBuilder(d)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(d, "public", ogName)
	read := func() []byte {
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := read()
	img, err := png.Decode(bytes.NewReader(first))
	if err != nil || img.Bounds().Dx() != ogW || img.Bounds().Dy() != ogH {
		t.Fatalf("og-image: %v %v", err, img.Bounds())
	}
	if c := color.NRGBAModel.Convert(img.At(ogW/2, ogH-1)).(color.NRGBA); c.B < 200 || c.R > 0x17 || c.G > 0x68 {
		t.Fatalf("accent bar %v, want the favicon blue darkened to text contrast", c)
	}
	if c := color.NRGBAModel.Convert(img.At(4, 4)).(color.NRGBA); c.B <= c.R || c.R < 235 {
		t.Fatalf("background %v is not a light accent tint", c)
	}
	past := time.Now().Add(-time.Hour)
	os.Chtimes(out, past, past)
	if _, err := NewBuilder(d).Build(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); !fi.ModTime().Equal(past) {
		t.Fatal("an unchanged og-image was rendered again")
	}
	edit(t, d, "content/nb/_index.md", "description:", "summary: Ny *oppsummering*\ndescription:")
	if _, err := NewBuilder(d).Build(); err != nil {
		t.Fatal(err)
	}
	second := read()
	if bytes.Equal(first, second) {
		t.Fatal("changing the home summary kept the old og-image")
	}
	edit(t, d, "site.yml", "title: Eksempel", "title: Nytt navn")
	if _, err := NewBuilder(d).Build(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(second, read()) {
		t.Fatal("changing the site title kept the old og-image")
	}
}

func TestOGText(t *testing.T) {
	if got := inlinePlain("Regnskap **gjort** [enkelt](/om/ \"Om\") -- `nå`"); got != "Regnskap gjort enkelt – nå" {
		t.Fatalf("plain summary %q", got)
	}
	icon, err := parseIcon([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><defs><linearGradient id="g"><stop stop-color="#fde047"/><stop offset="1" stop-color="#f9a8d4"/></linearGradient></defs><rect width="64" height="64" rx="14" fill="url(#g)"/><path d="M20 20h24v24H20z" fill="none" stroke="#5b3fe0" stroke-width="4"/><circle cx="32" cy="32" r="4" fill="#fff"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if a := icon.accent(); a != (color.NRGBA{0x5b, 0x3f, 0xe0, 255}) {
		t.Fatalf("accent %v, want the saturated stroke over the pale gradient", a)
	}
	long := ogText{host: "example.no", title: "Et svært langt nettstedsnavn som ikke får plass på én linje i bildet", tagline: string(bytes.Repeat([]byte("Regnskap for små bedrifter og foreninger. "), 20))}
	gen, err := prepareOGImage(long, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#fff"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(gen())); err != nil {
		t.Fatal(err)
	}
	bold := fontFace(true)
	col := float64(ogW - 2*ogMargin - ogIcon - ogGap)
	lines := wrapLines(long.tagline, ogTagLines, func(s string) bool { return fontFace(false).measure(s, ogTagPx) <= col })
	if len(lines) != ogTagLines || lines[len(lines)-1][len(lines[len(lines)-1])-3:] != "…" {
		t.Fatalf("tagline not capped: %q", lines)
	}
	if w := bold.measure("AV", 100); w >= bold.advance('A')*100+bold.advance('V')*100 {
		t.Fatalf("no kerning: %v", w)
	}
}
