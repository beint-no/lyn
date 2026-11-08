package build

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "public" || rel == ".lyn" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func site(t testing.TB) string {
	dir := t.TempDir()
	copyTree(t, "../example", dir)
	return dir
}

func edit(t testing.TB, dir, file, old, new string) {
	t.Helper()
	p := filepath.Join(dir, file)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(old)) {
		t.Fatalf("%s does not contain %q", file, old)
	}
	if err := os.WriteFile(p, bytes.Replace(b, []byte(old), []byte(new), 1), 0o644); err != nil {
		t.Fatal(err)
	}
}

var touched = map[string]bool{}
var structural bool

func write(t testing.TB, dir, file, content string) {
	t.Helper()
	p := filepath.Join(dir, file)
	if !exists(p) {
		structural = true
	}
	touched[p] = true
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildErr(dir string) error {
	b := NewBuilder(dir)
	b.Persist = false
	_, err := b.Build()
	return err
}

func TestExampleBuilds(t *testing.T) {
	if err := buildErr(site(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAccepts(t *testing.T) {
	cases := []struct {
		name string
		mut  func(t *testing.T, d string)
	}{
		{"nested css", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", ".prose table { border-collapse: collapse; }", ".prose { & :is(h2, h3) a::after { content: \"#\"; } table { border-collapse: collapse; } .nested { color: red; } }")
			edit(t, d, "layouts/about.html", "<h1>", `<p class="nested">x</p><h1>`)
		}},
		{"boolean attribute in branch", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `<input class="input" name="email" type="email">`, `<input class="input" name="email" type="email"{{ if .Title }} required{{ end }}>`)
		}},
		{"return pipeline", func(t *testing.T, d string) {
			write(t, d, "layouts/partials/label.html", `{{ return printf "%s!" .Title | upper }}`)
			edit(t, d, "layouts/about.html", "<h1>{{ .Title }}</h1>", `<h1 title="{{ partial "label" . }}">{{ .Title }}</h1>`)
		}},
		{"tool params", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "pageParams:", "toolParams:\n  coveredIntents: list\npageParams:")
			edit(t, d, "content/nb/guider/lonn/index.md", "weight: 2", "weight: 2\ncoveredIntents: [lonn, skatt]")
		}},
		{"data attribute queried by inline script", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<h1 data-free-test="1">`)
			edit(t, d, "layouts/about.html", "</form>", `</form><script>document.querySelector('[data-free-test]')</script>`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := site(t)
			c.mut(t, d)
			if err := buildErr(d); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSummaryAndSectionDates(t *testing.T) {
	d := site(t)
	edit(t, d, "content/nb/guider/lonn/index.md", "summary: Lønn steg for steg.", "summary: Lønn \"steg\" for *steg*.")
	edit(t, d, "layouts/partials/card.html", "<p>{{ .Summary }}</p>", `<p title="{{ .Summary }}">{{ .Summary }}</p>`)
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "public/guider/index.html"))
	if !strings.Contains(string(b), `<p title="Lønn “steg” for steg.">Lønn &ldquo;steg&rdquo; for <em>steg</em>.</p>`) {
		t.Errorf("summary not rendered as inline Markdown:\n%s", b)
	}
	sm, _ := os.ReadFile(filepath.Join(d, "public/sitemap.xml"))
	if !strings.Contains(string(sm), "<loc>https://example.no/guider/</loc><lastmod>2026-10-01</lastmod>") {
		t.Errorf("section lastmod should be its newest page:\n%s", sm)
	}
}

func TestMarkdownDetails(t *testing.T) {
	d := site(t)
	edit(t, d, "content/nb/guider/mva/index.md", "## Satser", "## Pris- og \"driftsplikt\"\n\n## Satser")
	edit(t, d, "layouts/page.html", "{{ .Content }}", "{{ .TableOfContents }}{{ .Content }}")
	edit(t, d, "assets/site.css", ".meta, .more", "#TableOfContents, .meta, .more")
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "public/guider/mva/index.html"))
	for _, want := range []string{`id="pris--og-driftsplikt"`, `href="#pris--og-driftsplikt">Pris- og “driftsplikt”</a>`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in\n%s", want, b)
		}
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name string
		mut  func(t *testing.T, d string)
		want string
	}{
		{"markdown broken link", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "[x](/finnes-ikke/ \"x\") Lønn kjøres")
		}, "broken link"},
		{"template broken link", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/nope/">x</a><h1>`)
		}, "broken link /nope/"},
		{"missing trailing slash", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/guider">x</a><h1>`)
		}, "must end with a slash"},
		{"missing fragment", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "/guider/mva/#satser", "/guider/mva/#nope")
		}, "missing #nope"},
		{"same-page fragment", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="#ghost">x</a><h1>`)
		}, "no element with that id"},
		{"unused css class", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "body {", ".ghost { color: red; }\nbody {")
		}, "CSS class .ghost is never used"},
		{"unstyled class", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `<h1>`, `<h1 class="unstyled">`)
		}, `class "unstyled" has no CSS rule`},
		{"unused custom property", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "--gap: 1rem;", "--gap: 1rem; --unused: 1px;")
		}, "custom property --unused is never read"},
		{"undefined custom property", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "font-weight: 700;", "font-weight: 700; color: var(--nope);")
		}, "--nope is never defined"},
		{"unused translation", func(t *testing.T, d string) {
			edit(t, d, "i18n/nb.yml", "read_more:", "unused_key: x\nread_more:")
		}, `translation "unused_key" is never used`},
		{"missing translation", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p>{{ T "nope" }}</p><h1>`)
		}, `"nope" does not exist`},
		{"unused config param", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "    email: post@example.no", "    email: post@example.no\n    phone: \"12345678\"")
		}, "params.contact.phone: value is never used"},
		{"unused front matter param", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "  badge: string", "  badge: string\n  extra: string")
			edit(t, d, "content/nb/guider/lonn/index.md", "weight: 2", "weight: 2\nextra: x")
		}, "lonn/index.md:7: front matter extra is never used"},
		{"undeclared front matter key", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "weight: 2", "weight: 2\nbadges: x")
		}, `unknown front matter key "badges"`},
		{"wrongly typed front matter", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/mva/index.md", "badge: Ny", "badge: true")
		}, "badge must be a string"},
		{"declared but unset page param", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "  badge: string", "  badge: string\n  ghost: bool")
		}, "pageParams.ghost is declared but no page sets it"},
		{"unused partial", func(t *testing.T, d string) {
			write(t, d, "layouts/partials/ghost.html", "<p>x</p>")
		}, "partial is never used"},
		{"unused layout", func(t *testing.T, d string) {
			write(t, d, "layouts/ghost.html", `{{ define "main" }}<p>x</p>{{ end }}`)
		}, "layout is never used"},
		{"unused shortcode", func(t *testing.T, d string) {
			write(t, d, "layouts/shortcodes/ghost.html", `<p>x</p>`)
		}, "shortcode is never used"},
		{"unused bundle file", func(t *testing.T, d string) {
			write(t, d, "content/nb/guider/lonn/x.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
		}, "bundle file is never used"},
		{"unused static file", func(t *testing.T, d string) {
			write(t, d, "static/img/x.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
		}, "static/img/x.svg: file is never referenced"},
		{"missing image", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "![x](nope.svg)\n\nLønn kjøres")
		}, "does not exist in the page bundle"},
		{"unknown field", func(t *testing.T, d string) {
			edit(t, d, "layouts/page.html", "{{ .Title }}", "{{ .Titel }}")
		}, `unknown field .Titel (did you mean "Title"?)`},
		{"unquoted attribute", func(t *testing.T, d string) {
			edit(t, d, "layouts/partials/card.html", `href="{{ .RelPermalink }}"`, `href={{ .RelPermalink }}`)
		}, "must be double-quoted"},
		{"script context", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script>var t = {{ .Title }}</script><h1>`)
		}, "only json output"},
		{"inline handler", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<h1 onclick="x()">`)
		}, "inline event handler"},
		{"template comment", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `{{/* note */}}<h1>`)
		}, "comments are not allowed"},
		{"unknown front matter key", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "description:", "descripton: x\ndescription:")
		}, `unknown front matter key "descripton" (did you mean "description"?)`},
		{"yaml comment", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "weight: 2", "weight: 2 # second")
		}, "comments are not allowed"},
		{"duplicate title", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "title: Lønn for nybegynnere", "title: Slik fungerer mva")
		}, "duplicate <title>"},
		{"missing description", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "description: Hvordan du kjører lønn første gang.\n", "")
		}, `missing required key "description"`},
		{"noindex without meta", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "weight: 2", "weight: 2\nnoindex: true")
		}, "noindex page must render"},
		{"folder without section", func(t *testing.T, d string) {
			write(t, d, "content/nb/blogg/innlegg/index.md", "---\ntitle: Innlegg\ndescription: x\n---\nTekst.\n")
		}, "no _index.md"},
		{"unused data attribute", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<h1 data-foo="1">`)
		}, "attribute data-foo is never used"},
		{"img without alt", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg"><h1>`)
		}, "without alt"},
		{"insecure link", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "[x](http://example.com \"x\") Lønn kjøres")
		}, "insecure link"},
		{"redirect to missing page", func(t *testing.T, d string) {
			write(t, d, "static/_redirects", "/gammel/ /mangler/ 301\n")
		}, "broken link /mangler/"},
		{"redirect shadows page", func(t *testing.T, d string) {
			write(t, d, "static/_redirects", "/om/ /guider/ 301\n")
		}, "is redirected but the page still exists"},
		{"link through redirect", func(t *testing.T, d string) {
			write(t, d, "static/_redirects", "/gammel/ /guider/ 301\n")
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/gammel/">x</a><h1>`)
		}, "goes through a redirect"},
		{"visible markdown", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "**Lønn kjøres")
		}, "visible markup"},
		{"body never rendered", func(t *testing.T, d string) {
			edit(t, d, "content/nb/om/index.md", "layout: about\n---\n", "layout: about\n---\nTekst som aldri vises.\n")
		}, "never rendered"},
		{"unbalanced tags", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>{{ .Title }}</h1>", "<h1>{{ .Title }}</h2>")
		}, "unexpected </h2>"},
		{"branch context mismatch", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" {{ if .Title }}href="/"{{ else }}href="/{{ end }}">x</a><h1>`)
		}, "branches end in different HTML contexts"},
		{"wrong menu pageRef", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "pageRef: /om", "pageRef: /about")
		}, "is not a nb page"},
		{"duplicate id", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p id="kontakt">x</p><h1>`)
		}, `duplicate id "kontakt"`},
		{"non-ascii folder", func(t *testing.T, d string) {
			write(t, d, "content/nb/guider/lønn-2/index.md", "---\ntitle: X\ndescription: x\n---\nTekst.\n")
		}, "lowercase ASCII"},
		{"standalone markdown", func(t *testing.T, d string) {
			write(t, d, "content/nb/guider/side.md", "---\ntitle: X\ndescription: x\n---\nTekst.\n")
		}, "must be index.md"},
		{"content read inside its own shortcode", func(t *testing.T, d string) {
			edit(t, d, "layouts/shortcodes/tip.html", "<strong>", "<strong>{{ .Page.WordCount }}")
		}, "reads its own content"},
		{"unused asset", func(t *testing.T, d string) {
			write(t, d, "assets/extra.css", ".x{color:red}")
		}, "assets/extra.css: asset is never used"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := site(t)
			c.mut(t, d)
			err := buildErr(d)
			if err == nil {
				t.Fatalf("build succeeded; want error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got:\n%v\nwant error containing %q", err, c.want)
			}
		})
	}
}

func snapshot(t testing.TB, dir string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func errLines(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type mutation struct {
	name string
	fn   func(t testing.TB, d string, r *rand.Rand, n int)
}

func pageFiles(d string) []string {
	var out []string
	filepath.WalkDir(filepath.Join(d, "content"), func(p string, e fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".md") {
			rel, _ := filepath.Rel(d, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func extraPages(d string) []string {
	var out []string
	for _, f := range pageFiles(d) {
		if strings.Contains(f, "/ekstra-") {
			out = append(out, f)
		}
	}
	return out
}

var mutations = []mutation{
	{"body", func(t testing.TB, d string, r *rand.Rand, n int) {
		files := pageFiles(d)
		f := files[r.IntN(len(files))]
		b, _ := os.ReadFile(filepath.Join(d, f))
		if bytes.Contains(b, []byte("layout: about")) {
			return
		}
		write(t, d, f, string(b)+fmt.Sprintf("\nAvsnitt %d.\n", n))
	}},
	{"title", func(t testing.TB, d string, r *rand.Rand, n int) {
		files := pageFiles(d)
		f := files[r.IntN(len(files))]
		b, _ := os.ReadFile(filepath.Join(d, f))
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "title: ") {
				lines[i] = fmt.Sprintf("title: Tittel nummer %d", n)
				break
			}
		}
		write(t, d, f, strings.Join(lines, "\n"))
	}},
	{"weight", func(t testing.TB, d string, r *rand.Rand, n int) {
		f := "content/nb/guider/lonn/index.md"
		b, _ := os.ReadFile(filepath.Join(d, f))
		s := string(b)
		for _, w := range []string{"weight: 2", "weight: 1", "weight: 3"} {
			if strings.Contains(s, w) {
				s = strings.Replace(s, w, fmt.Sprintf("weight: %d", 1+r.IntN(3)), 1)
				break
			}
		}
		write(t, d, f, s)
	}},
	{"add page", func(t testing.TB, d string, r *rand.Rand, n int) {
		write(t, d, fmt.Sprintf("content/nb/guider/ekstra-%d/index.md", n), fmt.Sprintf("---\ntitle: Ekstra %d\ndescription: Ekstra side %d.\nsummary: Kort om %d.\nweight: %d\n---\nInnhold med [lenke](/guider/mva/#satser \"Mva\").\n", n, n, n, 1+r.IntN(4)))
	}},
	{"remove page", func(t testing.TB, d string, r *rand.Rand, n int) {
		ex := extraPages(d)
		if len(ex) == 0 {
			return
		}
		os.RemoveAll(filepath.Dir(filepath.Join(d, ex[r.IntN(len(ex))])))
		structural = true
	}},
	{"i18n", func(t testing.TB, d string, r *rand.Rand, n int) {
		write(t, d, "i18n/nb.yml", fmt.Sprintf("read_more: Les mer %d\nupdated: Oppdatert %%s\n", n))
	}},
	{"partial", func(t testing.TB, d string, r *rand.Rand, n int) {
		write(t, d, "layouts/partials/card.html", fmt.Sprintf(`<article class="card">
  <h2><a href="{{ .RelPermalink }}">{{ .Title }}</a></h2>
  <p>{{ .Summary }} %d</p>
  {{ with .Params.badge }}<span class="badge">{{ . }}</span>{{ end }}
</article>
`, n))
	}},
	{"css", func(t testing.TB, d string, r *rand.Rand, n int) {
		b, _ := os.ReadFile(filepath.Join(d, "assets/site.css"))
		write(t, d, "assets/site.css", string(b)+fmt.Sprintf("\n.card { margin: %dpx; }\n", n))
	}},
	{"svg", func(t testing.TB, d string, r *rand.Rand, n int) {
		write(t, d, "content/nb/guider/mva/mva.svg", fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d 200"><rect width="10" height="10"/></svg>`, 100+n))
	}},
	{"config", func(t testing.TB, d string, r *rand.Rand, n int) {
		b, _ := os.ReadFile(filepath.Join(d, "site.yml"))
		s := strings.Replace(string(b), "title: Eksempel", fmt.Sprintf("title: Eksempel %d", n), 1)
		s = strings.Replace(s, fmt.Sprintf("title: Eksempel %d %d", n, n), fmt.Sprintf("title: Eksempel %d", n), 1)
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "title: ") {
				lines[i] = fmt.Sprintf("title: Eksempel %d", n)
			}
		}
		write(t, d, "site.yml", strings.Join(lines, "\n"))
	}},
	{"break link", func(t testing.TB, d string, r *rand.Rand, n int) {
		ex := extraPages(d)
		if len(ex) == 0 {
			return
		}
		f := ex[r.IntN(len(ex))]
		b, _ := os.ReadFile(filepath.Join(d, f))
		s := strings.Replace(string(b), "#satser", "#borte", 1)
		if s == string(b) {
			s = strings.Replace(s, "#borte", "#satser", 1)
		}
		write(t, d, f, s)
	}},
	{"delete public", func(t testing.TB, d string, r *rand.Rand, n int) {
		os.RemoveAll(filepath.Join(d, "public"))
	}},
	{"heading", func(t testing.TB, d string, r *rand.Rand, n int) {
		f := "content/nb/guider/mva/index.md"
		b, _ := os.ReadFile(filepath.Join(d, f))
		s := string(b)
		if strings.Contains(s, "## Satser") {
			s = strings.Replace(s, "## Satser", "## Prosenter", 1)
		} else {
			s = strings.Replace(s, "## Prosenter", "## Satser", 1)
		}
		write(t, d, f, s)
	}},
}

func TestIncrementalEqualsCold(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { incrementalEqualsCold(t, false, seed) })
	}
}

func TestServeHintsEqualCold(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { incrementalEqualsCold(t, true, seed) })
	}
}

func incrementalEqualsCold(t *testing.T, hinted bool, seed uint64) {
	steps := 60
	if testing.Short() {
		steps = 15
	}
	r := rand.New(rand.NewPCG(seed, 2))
	inc := site(t)
	b := NewBuilder(inc)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	for n := range steps {
		m := mutations[r.IntN(len(mutations))]
		touched, structural = map[string]bool{}, false
		m.fn(t, inc, r, n)
		if hinted {
			for f := range touched {
				b.Changed(f, structural)
			}
			if structural {
				b.Changed("", true)
			}
		}
		res, incErr := b.Build()
		cold := t.TempDir()
		copyTree(t, inc, cold)
		cb := NewBuilder(cold)
		cb.Persist = false
		_, coldErr := cb.Build()
		if errLines(incErr) != errLines(coldErr) {
			t.Fatalf("step %d (%s): errors differ\nincremental: %v\ncold: %v", n, m.name, incErr, coldErr)
		}
		if coldErr != nil {
			continue
		}
		a, c := snapshot(t, filepath.Join(inc, "public")), snapshot(t, filepath.Join(cold, "public"))
		for k, v := range c {
			if a[k] != v {
				t.Fatalf("step %d (%s): %s differs (incremental %v)\nincremental:\n%s\ncold:\n%s", n, m.name, k, res, a[k], v)
			}
		}
		for k := range a {
			if _, ok := c[k]; !ok {
				t.Fatalf("step %d (%s): stale file %s in incremental output", n, m.name, k)
			}
		}
	}
}

func TestTruncate(t *testing.T) {
	for in, want := range map[[2]any]string{
		{9, "ab cdefgh ij"}: "ab cdefgh …",
		{40, "behålla säkerhetsmarginal och kunna motivera"}: "behålla säkerhetsmarginal och kunna …",
		{20, "kort"}:  "kort",
		{3, "abcdef"}: "abc …",
	} {
		if got := truncate(in[1].(string), in[0].(int)); got != want {
			t.Errorf("truncate(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestWordCount(t *testing.T) {
	if n := len(strings.Fields(stripHTML("<p>One <strong>two</strong>, three “four” five.</p>\n<p>Six</p>"))); n != 6 {
		t.Fatalf("got %d words", n)
	}
}

func bilingual(t *testing.T) string {
	d := site(t)
	edit(t, d, "site.yml", "    label: Norsk\n", "    label: Norsk\n")
	b, _ := os.ReadFile(filepath.Join(d, "site.yml"))
	cfg := strings.Replace(string(b), "pageParams:", "  - code: en\n    locale: en-GB\n    label: English\npageParams:", 1)
	write(t, d, "site.yml", cfg)
	nb, _ := os.ReadFile(filepath.Join(d, "i18n/nb.yml"))
	write(t, d, "i18n/en.yml", string(nb))
	write(t, d, "content/en/_index.md", "---\ntitle: Accounting made easy\nseoTitle: Accounting | Example\ndescription: Guides.\n---\n")
	write(t, d, "content/en/guides/_index.md", "---\ntitle: Guides\ndescription: All guides.\ntranslationOf: /guider/\n---\n")
	write(t, d, "content/en/guides/vat/index.md", "---\ntitle: How VAT works\ndescription: VAT explained.\nsummary: All about VAT.\ntranslationOf: /guider/mva/\n---\nVAT text.\n")
	write(t, d, "content/en/about/index.md", "---\ntitle: About us\ndescription: Who we are.\nlayout: about\ntranslationOf: /om/\n---\n")
	return d
}

func TestTranslationOf(t *testing.T) {
	d := bilingual(t)
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	sm, _ := os.ReadFile(filepath.Join(d, "public/sitemap.xml"))
	if !strings.Contains(string(sm), `hreflang="en-GB" href="https://example.no/en/guides/vat/"`) {
		t.Fatalf("translations not linked:\n%s", sm)
	}
	edit(t, d, "content/en/guides/vat/index.md", "translationOf: /guider/mva/", "translationOf: /guider/finnes-ikke/")
	if err := buildErr(d); err == nil || !strings.Contains(err.Error(), "translationOf /guider/finnes-ikke/ is not a nb page") {
		t.Fatalf("got %v", err)
	}
}

func TestMemoKeysNeverCollide(t *testing.T) {
	seen := map[string]int{}
	lang := &Language{Weight: 0}
	nf := &Page{idx: -1, Lang: lang}
	seen[string(appendPageKey(nil, nf))] = -1
	seen[string(appendPageKey(nil, nil))] = -2
	for i := range 70000 {
		k := string(appendPageKey(nil, &Page{idx: int32(i), Lang: lang}))
		if o, dup := seen[k]; dup {
			t.Fatalf("page %d collides with %d", i, o)
		}
		seen[k] = i
	}
}
