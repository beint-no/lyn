package build

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIcons(t *testing.T) {
	d := site(t)
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(d, "public/apple-touch-icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != touchIconSize || img.Bounds().Dy() != touchIconSize {
		t.Fatalf("touch icon: %v %v", err, img.Bounds())
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0xffff {
		t.Fatal("touch icon corner is transparent")
	}
	ico, err := os.ReadFile(filepath.Join(d, "public/favicon.ico"))
	if err != nil || !bytes.HasPrefix(ico, []byte{0, 0, 1, 0, 1, 0, faviconSize, faviconSize}) || !bytes.Contains(ico, []byte("\x89PNG")) {
		t.Fatalf("favicon.ico: %v % x", err, ico[:min(len(ico), 16)])
	}
}

func TestIconFailures(t *testing.T) {
	cases := []struct{ name, file, content, want string }{
		{"static raster", "static/favicon.ico", "x", "static/favicon.ico: lyn renders it from static/favicon.svg"},
		{"text", "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#fff"/><text>A</text></svg>`, "<text> is not supported in icons"},
		{"not square", "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 32"><rect width="64" height="32" fill="#fff"/></svg>`, "must be a square"},
		{"bad path", "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><path d="M1 2 L3"/></svg>`, "command L needs 2 numbers"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := site(t)
			write(t, d, c.file, c.content)
			err := buildErr(d)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
	d := site(t)
	os.Remove(filepath.Join(d, "static/favicon.svg"))
	if err := buildErr(d); err == nil || !strings.Contains(err.Error(), "static/favicon.svg is required") {
		t.Fatalf("missing favicon: %v", err)
	}
}

func TestIconGeometry(t *testing.T) {
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="#000"/><stop offset="1" stop-color="#fff"/></linearGradient></defs><rect width="64" height="64" rx="16" fill="url(#g)"/><g fill="none" stroke="#f00" stroke-width="8" stroke-linecap="round" stroke-linejoin="round"><path d="M16 32h32"/><path d="m20 20 a12 12 0 1 1 24 0"/></g></svg>`)
	icon, err := parseIcon(src)
	if err != nil {
		t.Fatal(err)
	}
	img := icon.render(64, false)
	at := func(x, y int) [4]uint8 {
		i := img.PixOffset(x, y)
		return [4]uint8(img.Pix[i : i+4])
	}
	if c := at(32, 32); c[0] < 250 || c[1] > 5 || c[3] != 255 {
		t.Fatalf("stroke centre %v", c)
	}
	if c := at(32, 37); c[0] == 255 && c[1] == 0 {
		t.Fatalf("stroke wider than its width: %v", c)
	}
	if c := at(0, 0); c[3] != 0 {
		t.Fatalf("rounded corner painted: %v", c)
	}
	if a, b := at(8, 8), at(56, 56); a[0] >= b[0] {
		t.Fatalf("gradient not increasing: %v %v", a, b)
	}
}

func TestRenderErrorsAreNotBuried(t *testing.T) {
	d := site(t)
	edit(t, d, "layouts/page.html", "{{ .Title }}", "{{ .Title }}{{ link \"/nope.png\" }}")
	err := buildErr(d)
	if err == nil || !strings.Contains(err.Error(), "nope.png") || strings.Contains(err.Error(), "never used") {
		t.Fatalf("want only the render error, got %v", err)
	}
}

func TestAssetHeaders(t *testing.T) {
	d := site(t)
	write(t, d, "static/_headers", "/*\n  X-Content-Type-Options: nosniff\n")
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	h, _ := os.ReadFile(filepath.Join(d, "public/_headers"))
	if string(h) != "/*\n  X-Content-Type-Options: nosniff\n\n/_assets/*\n  Cache-Control: public, max-age=31536000, immutable\n" {
		t.Fatalf("headers:\n%s", h)
	}
	css, _ := filepath.Glob(filepath.Join(d, "public/_assets/site.min.*.css"))
	if len(css) != 1 {
		t.Fatalf("fingerprinted CSS not under /_assets/: %v", css)
	}
	write(t, d, "static/_headers", "/_assets/*\n  Cache-Control: no-store\n")
	if err := buildErr(d); err == nil || !strings.Contains(err.Error(), "lyn already caches /_assets/* immutably") {
		t.Fatalf("want duplicate asset rule error, got %v", err)
	}
}

func TestSuggestFileName(t *testing.T) {
	for in, want := range map[string]string{"312_0-2.2_y.svg": "312-0-2-2-y.svg", "Diagram_A.svg": "diagram-a.svg", "y4t0flWdSI0.webp": "y4t0flwdsi0.webp", "--x--": "x"} {
		got := strings.TrimPrefix(suggestFileName(in), "e.g. ")
		if got != want || !fileSegRE.MatchString(got) {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
