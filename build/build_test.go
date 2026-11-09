package build

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
		{"percent-encoded fragment", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/mva/index.md", "## Satser", "## Særlige satser\n\nSe [her](#s%C3%A6rlige-satser \"Satser\").\n\n## Satser")
		}},
		{"attribute before a conditional attribute", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `<input class="input" name="email" type="email">`, `<input class="input" name="email" type="email" required {{ if .Title }}autofocus{{ end }}>`)
			edit(t, d, "layouts/about.html", "</form>", `</form>{{ if not (hasSuffix (printf "%s" "x") "y") }}{{ end }}`)
		}},
		{"noindex page nothing links to", func(t *testing.T, d string) {
			write(t, d, "content/nb/hemmelig/index.md", "---\ntitle: Hemmelig\ndescription: Ingen lenker hit.\nlayout: about\nnoindex: true\n---\n")
			edit(t, d, "layouts/base.html", "<title>", `{{ if .NoIndex }}<meta name="robots" content="noindex">{{ end }}<title>`)
		}},
		{"tool params", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "pageParams:", "toolParams:\n  coveredIntents: list\npageParams:")
			edit(t, d, "content/nb/guider/lonn/index.md", "weight: 2", "weight: 2\ncoveredIntents: [lonn, skatt]")
		}},
		{"data attribute queried by inline script", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<h1 data-free-test="1">`)
			edit(t, d, "layouts/about.html", "</form>", `</form><script>document.querySelector('[data-free-test]')</script>`)
		}},
		{"accessible names and script-filled elements", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<button class="more" type="button" title="Lukk"><svg viewBox="0 0 1 1"></svg></button><a class="more" href="/guider/"><svg viewBox="0 0 1 1"><title>Guider</title></svg></a><a class="more" href="/guider/"><img src="/favicon.svg" alt="Guider" width="64" height="64"></a><h2 id="svar"></h2><h2 data-result></h2><template><a class="more" href="/guider/"></a></template><iframe src="https://example.com/" title="Kart"></iframe><h1>`)
			edit(t, d, "layouts/about.html", "</form>", `</form><script>document.querySelector('[data-result]')</script>`)
		}},
		{"contact links and structured data", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="tel:+4712345678">Ring</a><a class="more" href="mailto:post@example.no,salg@example.no?subject=Hei%20der">E-post</a><script type="application/ld+json">{"@context": "https://schema.org", "@type": "Organization", "name": "Eksempel", "sameAs": ["https://example.no/"]}</script><label for="kontakt">Kontakt</label><h1>`)
		}},
		{"hreflang to self and other domains", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<link rel="icon"`, `<link rel="alternate" hreflang="nb-NO" href="{{ .Permalink }}"><link rel="alternate" hreflang="sv-SE" href="https://example.se/"><link rel="icon"`)
		}},
		{"svg with doctype, entities and cdata", func(t *testing.T, d string) {
			write(t, d, "content/nb/guider/mva/mva.svg", "<?xml version=\"1.0\"?>\n<!DOCTYPE svg [<!ENTITY ns \"http://www.w3.org/2000/svg\">]>\n<!-- logo -->\n<svg xmlns=\"&ns;\" viewBox=\"0,0,64,64\"><style><![CDATA[.a{fill:#000}]]></style><use href=\"#c\"/><text>A &amp; B &#38; &#x26;</text><circle id=\"c\" class='a' r=\"3\"/></svg>\n")
		}},
		{"valid content model", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<div class="more"><a class="more" href="/"><div class="more">x</div></a><ul class="more"><template><li>x</li></template><li>y</li></ul><template id="fact"><div class="more"><dt>a</dt><dd>b</dd></div></template><dl class="more"><div class="more"><dt>a</dt><dd>b</dd></div></dl><p class="more"><button class="more" type="button"><span>x</span></button></p><details class="more"><summary>x</summary><p class="more">y</p></details><table class="more"><thead><tr><th scope="col">a</th></tr></thead><tbody><tr><td colspan="2">b</td></tr></tbody></table></div><h1>`)
		}},
		{"valid attributes and values", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<my-widget class="more" foo="1">x</my-widget><svg class="more" viewBox="0 0 24 24" stroke-width="2" fill="none"><path d="M0 0h1"/></svg><button class="more" type="button" popovertarget="pop" popovertargetaction="toggle">x</button><div class="more" id="pop" popover>y</div><a class="more" href="https://example.com:8443/a?b=c#d" target="_blank" rel="noopener noreferrer">x</a><img src="/favicon.svg" srcset="/favicon.svg 64w, /favicon.svg 128w" sizes="(width >= 40rem) 50vw, 100vw" alt="" width="65" height="64" loading="lazy" decoding="async" fetchpriority="low"><img src="/favicon.svg" srcset="/favicon.svg 1x, /favicon.svg 2x" alt="" width="32" height="32"><a class="more" href="mailto:post@example.no?subject=Hei%20der&amp;body=Takk" hreflang="nb-NO" lang="nb">x</a><p class="more" hidden="until-found" dir="auto" translate="no" spellcheck="false" tabindex="-1">x</p><h1>`)
		}},
		{"valid form controls", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `<input class="input" name="email" type="email">`, `<input class="input" name="email" type="email" autocomplete="section-a shipping work email" inputmode="email" enterkeyhint="send" required="required" placeholder="ola@example.no TODO"><input class="input" name="tel" type="tel" autocomplete="mobile tel"><select class="input" name="x" autocomplete="off"><optgroup label="A"><option value="1" selected>1</option></optgroup></select><textarea class="input" name="y" rows="3"></textarea><button class="input" type="submit" formmethod="post">x</button>`)
		}},
		{"valid text", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Kodingsfeil vises som `MÃ¸ller` eller\n\n```\n| a | b |\n|---|---|\nTODO: Lorem ipsum [^1] [a][b] &amp;nbsp;\n```\n\nTODOS og TBDX er ord. Lønn kjøres")
		}},
		{"valid css", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "body {", "@property --angle { syntax: \"<angle>\"; inherits: false; initial-value: 0deg; }\n@font-face { font-family: X; src: url(\"/favicon.svg\"); font-display: swap; }\n@page { size: A4; margin: 1cm; }\n@keyframes fade { from { opacity: 0; } 50% { opacity: 0.5; } }\n@layer base;\n@view-transition { navigation: auto; }\n.prose { -webkit-text-size-adjust: none; text-wrap: balance; display: inline flex; position: sticky !important; height: 100vh; height: 100dvh; @media (width > 1px) { color: red; } &:is(:hover, :focus-visible)::after { content: \"x\"; } &::-webkit-scrollbar { display: none; } &:has(> p:nth-child(2n + 1 of .meta)) { interpolate-size: allow-keywords; field-sizing: content; anchor-name: --a; position-try-fallbacks: flip-block; view-transition-name: x; } }\n#kontakt { scroll-margin-top: 1rem; }\nbody {")
		}},
		{"valid scripts and dates", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script type="module">import { a } from "/x.js"; const el = document.getElementById("kontakt"); document.querySelector("#kontakt .x"); const t = "#" + a;</script><h1>`)
			edit(t, d, "content/nb/guider/lonn/index.md", "date: 2026-09-01", "date: 2026-09-01\nlastmod: 2026-09-02")
		}},
		{"valid head and headings", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<link rel="icon"`, `<meta name="theme-color" content="#fff" media="(prefers-color-scheme: light)"><meta name="theme-color" content="#000" media="(prefers-color-scheme: dark)"><meta property="og:image" content="https://example.no/favicon.svg"><meta property="og:image" content="https://cdn.example.com/b.png"><link rel="icon"`)
			edit(t, d, "layouts/base.html", `<footer class="footer">`, `<footer class="footer"><h4>Bunn</h4>`)
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "## A\n\n### B\n\n## C\n\nLønn kjøres")
		}},
		{"headers rules", func(t *testing.T, d string) {
			write(t, d, "static/_headers", "/*\n  X-Content-Type-Options: nosniff\n  ! X-Robots-Tag\n\n/guider/:slug/\n  X-Frame-Options: DENY\nhttps://example.no/om/\n  X-Frame-Options: DENY\n")
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
			edit(t, d, "i18n/en.yml", "read_more:", "unused_key: x\nread_more:")
		}, `translation "unused_key" is never used`},
		{"translations on a single-language site", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "  - code: en\n    locale: en-GB\n    label: English\n", "  - code: xx\n    locale: en-GB\n    label: English\n")
			b, _ := os.ReadFile(filepath.Join(d, "site.yml"))
			write(t, d, "site.yml", string(b[:strings.Index(string(b), "  - code: xx")])+string(b[strings.Index(string(b), "pageParams:"):]))
			os.RemoveAll(filepath.Join(d, "content/en"))
			os.Remove(filepath.Join(d, "i18n/en.yml"))
		}, "translations are only for multilingual sites"},
		{"seoTitle repeating title", func(t *testing.T, d string) {
			edit(t, d, "content/nb/om/index.md", "title: Om oss\n", "title: Om oss\nseoTitle: Om oss\n")
		}, "content/nb/om/index.md:3: seoTitle repeats title"},
		{"lastmod repeating date", func(t *testing.T, d string) {
			edit(t, d, "content/nb/om/index.md", "title: Om oss\n", "title: Om oss\ndate: 2026-01-02\nlastmod: 2026-01-02\n")
		}, "content/nb/om/index.md:4: lastmod equals date"},
		{"unused menu", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "          pageRef: /om\n          weight: 2\n", "          pageRef: /om\n          weight: 2\n      footer:\n        - name: Guider\n          pageRef: /guider\n")
		}, "menus.nb.footer is never used by any template"},
		{"unused data key", func(t *testing.T, d string) {
			write(t, d, "data/team.json", "{\n  \"name\": \"Ola\",\n  \"role\": \"CEO\"\n}\n")
			edit(t, d, "layouts/about.html", "<h1>", "<p>{{ site.Data.team.name }}</p><h1>")
		}, "data/team.json:3: key role is never used"},
		{"duplicate data key", func(t *testing.T, d string) {
			write(t, d, "data/team.json", "{\n  \"name\": \"Ola\",\n  \"name\": \"Kari\"\n}\n")
		}, `data/team.json:3: duplicate key "name"`},
		{"data syntax error", func(t *testing.T, d string) {
			write(t, d, "data/team.json", "{\n  \"name\": \"Ola\",\n  \"role\" \"CEO\"\n}\n")
		}, "data/team.json:3:"},
		{"uppercase file name", func(t *testing.T, d string) {
			write(t, d, "content/nb/guider/mva/Diagram_A.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
		}, "content/nb/guider/mva/Diagram_A.svg: file and folder names must be lowercase words joined by hyphens (e.g. diagram-a.svg)"},
		{"raster that is not AVIF", func(t *testing.T, d string) {
			write(t, d, "static/photo.webp", "x")
		}, "static/photo.webp: raster images must be AVIF; convert it to photo.avif"},
		{"orphan page", func(t *testing.T, d string) {
			write(t, d, "content/nb/hemmelig/index.md", "---\ntitle: Hemmelig\ndescription: Ingen lenker hit.\nlayout: about\n---\n")
		}, "content/nb/hemmelig/index.md: no other indexable page links to /hemmelig/"},
		{"missing translation", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p>{{ T "nope" }}</p><h1>`)
		}, `"nope" does not exist`},
		{"unused config param", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "    email: post@example.no", "    email: post@example.no\n    phone: \"12345678\"")
		}, "params.contact.phone is never used"},
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
		{"visible latex", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "$$\\text{lønn} = x$$ Lønn kjøres")
		}, "visible markup"},
		{"broken sibling link", func(t *testing.T, d string) {
			sib := filepath.Join(d, "sib")
			copyTree(t, "../example", sib)
			edit(t, d, "site.yml", "endpoints:", "siblings:\n  - baseURL: https://sister.no/\n    root: sib\nendpoints:")
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="https://sister.no/guider/mva/">ok</a><a class="more" href="https://sister.no/finnes-ikke/">x</a><h1>`)
		}, "broken link https://sister.no/finnes-ikke/"},
		{"static og-image", func(t *testing.T, d string) {
			write(t, d, "static/og-image.png", "x")
		}, "static/og-image.png: lyn renders it from the site title"},
		{"og-image glyph", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "title: Eksempel", "title: Eksempel 漢")
		}, "site.yml: title: the og-image font (Inter, Latin) has no glyph for '漢'"},
		{"og-image summary glyph", func(t *testing.T, d string) {
			edit(t, d, "content/nb/_index.md", "description:", "summary: Regnskap ☃\ndescription:")
		}, "content/nb/_index.md:4: the og-image font (Inter, Latin) has no glyph for '☃'"},
		{"unused asset", func(t *testing.T, d string) {
			write(t, d, "assets/extra.css", ".x{color:red}")
		}, "assets/extra.css: asset is never used"},
		{"svg without viewBox", func(t *testing.T, d string) {
			write(t, d, "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><circle cx="32" cy="32" r="30"/></svg>`)
		}, "static/favicon.svg:1: <svg> has no viewBox"},
		{"svg script", func(t *testing.T, d string) {
			write(t, d, "static/favicon.svg", "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 64 64\">\n<script>alert(1)</script></svg>")
		}, "static/favicon.svg:2: <script> is not allowed"},
		{"svg event handler", func(t *testing.T, d string) {
			write(t, d, "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" onload="x()"/>`)
		}, "event handler onload"},
		{"svg unescaped ampersand", func(t *testing.T, d string) {
			write(t, d, "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><text>A & B</text></svg>`)
		}, "unescaped & in text"},
		{"svg unbalanced", func(t *testing.T, d string) {
			write(t, d, "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><g><path d="M0 0"/></svg>`)
		}, "unexpected </svg>; expected </g>"},
		{"svg external reference", func(t *testing.T, d string) {
			write(t, d, "static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><image href="https://cdn.example.com/x.png"/></svg>`)
		}, "external reference https://cdn.example.com/x.png"},
		{"icon-only button", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<button class="more" type="button"><svg viewBox="0 0 1 1"></svg></button><h1>`)
		}, "<button> has no accessible text"},
		{"empty link", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/guider/"></a><h1>`)
		}, "<a> has no accessible text"},
		{"iframe without title", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<iframe src="https://example.com/"></iframe><h1>`)
		}, "<iframe> has no title"},
		{"empty heading", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<h2></h2><h1>`)
		}, "empty <h2>"},
		{"two h1", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<h1>Andre</h1><h1>`)
		}, "indexable page has 2 <h1> elements"},
		{"no h1", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>{{ .Title }}</h1>", `<h2>{{ .Title }}</h2>`)
		}, "layouts/about.html: indexable page has no <h1>"},
		{"duplicate description", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "description: Hvordan du kjører lønn første gang.", "description: En enkel forklaring av merverdiavgift.")
		}, "duplicate meta description"},
		{"label without target", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `<input class="input"`, `<label for="epost">E-post</label><input class="input"`)
		}, `<label for="epost"> points to no element with that id`},
		{"local phone link", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="tel:12 34 56 78">Ring</a><h1>`)
		}, "phone link tel:12 34 56 78 is not an international number"},
		{"broken email link", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "email: post@example.no", "email: post@example")
		}, `invalid address "post@example"`},
		{"invalid json-ld", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script type="application/ld+json">{"@context": "https://schema.org", "name": }</script><h1>`)
		}, "JSON-LD is not valid JSON"},
		{"json-ld without context", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script type="application/ld+json">{"@type": "Organization", "name": "X"}</script><h1>`)
		}, `JSON-LD needs "@context"`},
		{"empty json-ld value", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script type="application/ld+json">{"@context": "https://schema.org", "@type": "Organization", "telephone": ""}</script><h1>`)
		}, "JSON-LD value .telephone is empty"},
		{"canonical to another page", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `{{ if ne .Kind "404" }}<link rel="canonical" href="{{ .Permalink }}">{{ end }}`, `{{ if ne .Kind "404" }}<link rel="canonical" href="{{ site.Home.Permalink }}">{{ end }}`)
		}, "canonical link https://example.no/ is not this page's URL https://example.no/om/"},
		{"canonical on 404", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `{{ if ne .Kind "404" }}<link rel="canonical" href="{{ .Permalink }}">{{ end }}`, `<link rel="canonical" href="{{ .Permalink }}">`)
		}, "the 404 page must not have a canonical link"},
		{"hreflang to unrelated page", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<link rel="icon"`, `<link rel="alternate" hreflang="nb-NO" href="{{ .Permalink }}"><link rel="alternate" hreflang="x-default" href="https://example.no/om/"><link rel="icon"`)
		}, "hreflang x-default link https://example.no/om/ is neither this page nor one of its translations"},
		{"translation verb without value", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p>{{ T "updated" }}</p><h1>`)
		}, `translation "updated" contains a format verb (%s) but T gets no value`},
		{"headers rule without files", func(t *testing.T, d string) {
			write(t, d, "static/_headers", "/*\n  X-Content-Type-Options: nosniff\n/js/gone.*\n  Cache-Control: no-store\n")
		}, "static/_headers:3: rule /js/gone.* matches no file"},
		{"headers syntax", func(t *testing.T, d string) {
			write(t, d, "static/_headers", "/*\nX-Frame-Options: DENY\n")
		}, "static/_headers:1: rule /* sets no headers"},
		{"block inside paragraph", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p class="more"><div class="more">x</div></p><h1>`)
		}, "<div> inside <p>; browsers end the paragraph"},
		{"block inside phrasing", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<span class="more"><div class="more">x</div></span><h1>`)
		}, "<div> inside <span>"},
		{"nested links", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/">x <a class="more" href="/guider/">y</a></a><h1>`)
		}, "<a> inside <a>"},
		{"button inside link", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/">x <button class="more" type="button">y</button></a><h1>`)
		}, "<button> inside <a>"},
		{"nested form", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `<input class="input"`, `<form class="form" action="/submit" method="post"><input class="input" name="x"></form><input class="input"`)
		}, "<form> inside <form>"},
		{"list item outside list", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<div class="more"><li>x</li></div><h1>`)
		}, "<li> must be inside <ul> or <ol> or <menu>, not <div>"},
		{"list child", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<ul class="more"><div class="more">x</div></ul><h1>`)
		}, "<div> cannot be a child of <ul>"},
		{"text in list", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<ul class="more">x<li>y</li></ul><h1>`)
		}, `text "x" directly inside <ul>`},
		{"definition outside list", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<div class="more"><dt>x</dt></div><h1>`)
		}, "<dt> must be inside <dl>"},
		{"table cell outside row", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<table class="more"><td>x</td></table><h1>`)
		}, "<td> cannot be a child of <table>"},
		{"summary not first", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<details class="more"><p class="more">x</p><summary>y</summary></details><h1>`)
		}, "<summary> must be the first child of <details>"},
		{"meta in body", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<meta name="x" content="y"><h1>`)
		}, "<meta> outside <head> is ignored"},
		{"duplicate attribute", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p class="more" class="meta">x</p><h1>`)
		}, "has the attribute class twice"},
		{"unknown attribute", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" hreff="/">x</a><h1>`)
		}, `unknown attribute hreff on <a> (did you mean "href"?)`},
		{"unknown element", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<secton class="more">x</secton><h1>`)
		}, `unknown element <secton> (did you mean "section"?)`},
		{"input type", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `type="email"`, `type="emial"`)
		}, `invalid type="emial" on <input>; use a valid value (did you mean "email"?)`},
		{"button type", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<button class="more" type="sumbit">x</button><h1>`)
		}, `(did you mean "submit"?)`},
		{"form method", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `method="post"`, `method="pots"`)
		}, `invalid method="pots" on <form>`},
		{"rel token", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `rel="icon"`, `rel="icno"`)
		}, `unknown rel value "icno" on <link> (did you mean "icon"?)`},
		{"target typo", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="/" target="_new">x</a><h1>`)
		}, `target="_new" opens a window named`},
		{"loading value", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" alt="" width="64" height="64" loading="lasy"><h1>`)
		}, `invalid loading="lasy" on <img>; use one of eager, lazy (did you mean "lazy"?)`},
		{"autocomplete token", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `type="email"`, `type="email" autocomplete="emial"`)
		}, `invalid autocomplete="emial": unknown field "emial" (did you mean "email"?)`},
		{"boolean attribute value", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", `type="email"`, `type="email" required="false"`)
		}, `boolean attribute required="false" is on whatever its value is`},
		{"popover target", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<button class="more" type="button" popovertarget="menu">x</button><h1>`)
		}, `popovertarget="menu" points to no element with that id on this page`},
		{"language tag", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<html lang="{{ .Language.Code }}">`, `<html lang="nb_NO">`)
		}, `lang="nb_NO" is not a language tag`},
		{"positive tabindex", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p class="more" tabindex="3">x</p><h1>`)
		}, `tabindex="3" takes the element out of document order`},
		{"image size unit", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" alt="" width="100%" height="64"><h1>`)
		}, `width="100%" on <img> must be a whole number`},
		{"image without size", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" alt=""><h1>`)
		}, "<img> without width and height"},
		{"image aspect ratio", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" alt="" width="64" height="32"><h1>`)
		}, "<img> declares 64×32 but /favicon.svg is 64×64"},
		{"srcset without sizes", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" srcset="/favicon.svg 64w, /favicon.svg 128w" alt="" width="64" height="64"><h1>`)
		}, "srcset with width descriptors needs a sizes attribute"},
		{"srcset descriptor", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" srcset="/favicon.svg 2" alt="" width="64" height="64"><h1>`)
		}, `srcset descriptor "2" must be a width`},
		{"garbled text", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "LÃ¸nn kjøres")
		}, "lonn/index.md:8: garbled text"},
		{"placeholder text", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Lorem ipsum dolor. Lønn kjøres")
		}, "lonn/index.md:8: placeholder text in page"},
		{"todo in text", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "TODO: skriv mer. Lønn kjøres")
		}, "placeholder text in page"},
		{"literal entity", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Lønn&amp;nbsp;kjøres")
		}, "HTML entity &nbsp; is shown literally"},
		{"unrendered table", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "<div class=\"more\">\n| a | b |\n|---|---|\n</div>\n\nLønn kjøres")
		}, `visible markup "|--"`},
		{"undefined footnote", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Lønn[^1] kjøres")
		}, `visible markup "[^"`},
		{"undefined reference link", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Se [mva][satser]. Lønn kjøres")
		}, `visible markup "]["`},
		{"unrendered heading", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<div class="more">
## Tittel</div><h1>`)
		}, `visible markup "## "`},
		{"mistyped scheme", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="htps://example.com/">x</a><h1>`)
		}, "link htps://example.com/ has a mistyped scheme"},
		{"missing scheme", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="www.example.com">x</a><h1>`)
		}, "has no scheme"},
		{"invalid host", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="https://example/">x</a><h1>`)
		}, `has an invalid host "example"`},
		{"space in url", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="https://example.com/a b">x</a><h1>`)
		}, "must be percent-encoded"},
		{"mailto plus", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "email: post@example.no", "email: post@example.no?subject=Hei+der")
		}, "uses + in subject"},
		{"mailto field", func(t *testing.T, d string) {
			edit(t, d, "site.yml", "email: post@example.no", "email: post@example.no?subjet=Hei")
		}, `unknown field "subjet"`},
		{"redirect chain", func(t *testing.T, d string) {
			write(t, d, "static/_redirects", "/a/ /b/ 301\n/b/ /guider/ 301\n")
		}, "static/_redirects:1: /a/ redirects to /b/, which redirects again; point it straight at /guider/"},
		{"redirect loop", func(t *testing.T, d string) {
			write(t, d, "static/_redirects", "/a/ /b/ 301\n/b/ /a/ 301\n")
		}, "is part of a redirect loop"},
		{"unknown css property", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "font-weight: 700;", "font-weight: 700; colr: red;")
		}, `assets/site.css:4: unknown CSS property colr (did you mean "color"?)`},
		{"unknown at-rule", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "body {", "@meda (width > 1px) { body { color: red; } }\nbody {")
		}, `assets/site.css:2: unknown at-rule @meda (did you mean "media"?)`},
		{"duplicate declaration", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "font-weight: 700;", "font-weight: 700; font-weight: 700;")
		}, "duplicate declaration font-weight: 700"},
		{"dead id selector", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "body {", "#ghost { color: red; }\nbody {")
		}, "assets/site.css:2: CSS selector #ghost matches no element on any page"},
		{"inline script syntax", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script>if (</script><h1>`)
		}, "JavaScript syntax error"},
		{"static script syntax", func(t *testing.T, d string) {
			write(t, d, "static/app.js", "let x = ;\n")
			edit(t, d, "layouts/base.html", "</body>", `<script src="/app.js"></script></body>`)
		}, "static/app.js:1: JavaScript syntax error"},
		{"script id lookup", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<script>document.getElementById("ghost")</script><h1>`)
		}, "script looks up #ghost, but no page has an element with that id"},
		{"lastmod before date", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "date: 2026-09-01", "date: 2026-09-01\nlastmod: 2026-08-01")
		}, "lonn/index.md:6: lastmod 2026-08-01 is before date 2026-09-01"},
		{"future date", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "date: 2026-09-01", "date: 2999-09-01")
		}, "date 2999-09-01 is in the future"},
		{"missing doctype", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", "<!doctype html>\n", "")
		}, "does not start with <!doctype html>"},
		{"missing viewport", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<meta name="viewport" content="width=device-width, initial-scale=1">`, "")
		}, `has no <meta name="viewport"`},
		{"html without lang", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<html lang="{{ .Language.Code }}">`, "<html>")
		}, "<html> has no lang attribute"},
		{"duplicate meta", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<link rel="icon"`, `<meta name="robots" content="all"><meta name="robots" content="all"><link rel="icon"`)
		}, "<meta> robots appears twice"},
		{"relative og image", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", `<link rel="icon"`, `<meta property="og:image" content="/favicon.svg"><link rel="icon"`)
		}, "og:image must be an absolute https:// URL"},
		{"empty fragment link", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<a class="more" href="#">x</a><h1>`)
		}, "link to # goes nowhere"},
		{"unknown pseudo-class", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", ".brand {", ".brand:hovr {")
		}, `assets/site.css:4: unknown pseudo-class :hovr (did you mean ":hover"?)`},
		{"invalid css keyword", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "font-weight: 700;", "font-weight: 700; display: blokc;")
		}, `invalid value blokc for display (did you mean "block"?)`},
		{"markup in front matter description", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "description: Hvordan du kjører lønn første gang.", "description: Hvordan du kjører [lønn](/guider/) første gang.")
		}, `content/nb/guider/lonn/index.md:3: visible markup "](" in content=`},
		{"placeholder alt", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<img src="/favicon.svg" alt="TODO" width="64" height="64"><h1>`)
		}, `placeholder text in alt="TODO"`},
		{"text in table", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<table class="more">x<tr><td>y</td></tr></table><h1>`)
		}, `text "x" directly inside <table>`},
		{"skipped heading level", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "#### Detaljer\n\nLønn kjøres")
		}, "lonn/index.md:8: <h4> follows <h1> and skips a heading level"},
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
	{"favicon", func(t testing.TB, d string, r *rand.Rand, n int) {
		write(t, d, "static/favicon.svg", fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><circle cx="32" cy="32" r="30" fill="#%06x"/></svg>`, r.IntN(1<<24)))
	}},
	{"home summary", func(t testing.TB, d string, r *rand.Rand, n int) {
		f := "content/nb/_index.md"
		b, _ := os.ReadFile(filepath.Join(d, f))
		lines := strings.Split(string(b), "\n")
		lines = slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, "summary: ") })
		if r.IntN(3) > 0 {
			lines = slices.Insert(lines, 1, fmt.Sprintf("summary: Regnskap *enkelt* for %d bedrifter", n))
		}
		write(t, d, f, strings.Join(lines, "\n"))
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
	{"svg viewBox", func(t testing.TB, d string, r *rand.Rand, n int) {
		b, _ := os.ReadFile(filepath.Join(d, "static/favicon.svg"))
		s := string(b)
		if strings.Contains(s, ` viewBox="0 0 64 64"`) {
			s = strings.Replace(s, ` viewBox="0 0 64 64"`, ` width="64"`, 1)
		} else {
			s = strings.Replace(s, ` width="64"`, ` viewBox="0 0 64 64"`, 1)
		}
		write(t, d, "static/favicon.svg", s)
	}},
	{"template link", func(t testing.TB, d string, r *rand.Rand, n int) {
		b, _ := os.ReadFile(filepath.Join(d, "layouts/partials/card.html"))
		s := string(b)
		if strings.Contains(s, `<a class="more" href="/guider/lon/">x</a>`) {
			s = strings.Replace(s, `<a class="more" href="/guider/lon/">x</a>`, "", 1)
		} else {
			s = strings.Replace(s, "</article>", `<a class="more" href="/guider/lon/">x</a></article>`, 1)
		}
		write(t, d, "layouts/partials/card.html", s)
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
	past := time.Now().Add(-time.Hour)
	filepath.WalkDir(inc, func(p string, d fs.DirEntry, err error) error {
		return os.Chtimes(p, past, past)
	})
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
		} else {
			b = NewBuilder(inc)
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

func TestInlineOnly(t *testing.T) {
	for in, want := range map[string]string{
		"1. juli 2026 skifter": "1\\. juli 2026 skifter",
		"- punkt":              "\\- punkt",
		"*viktig* tekst":       "*viktig* tekst",
		"# overskrift":         "\\# overskrift",
		"linje\nto":            "linje to",
	} {
		if got := inlineOnly(in); got != want {
			t.Errorf("inlineOnly(%q) = %q, want %q", in, got, want)
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
	if n := len(strings.Fields(stripHTML("<p>One <strong>two</strong>, three “four” five.</p><h2>Six</h2><div><a>seven</a></div><script>var no = 1</script>"))); n != 7 {
		t.Fatalf("got %d words", n)
	}
}

func bilingual(t *testing.T) string {
	return site(t)
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

func TestTagWhitespaceBeforeAction(t *testing.T) {
	d := site(t)
	edit(t, d, "layouts/about.html", `<input class="input" name="email" type="email">`, `<input class="input" name="email" type="email" required {{ if .Title }}autofocus{{ end }}>`)
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "public/om/index.html"))
	if !strings.Contains(string(b), `required autofocus>`) {
		t.Fatalf("attributes glued:\n%s", b)
	}
}

func TestErrorLocations(t *testing.T) {
	cases := []struct {
		name string
		mut  func(t *testing.T, d string)
		want []string
	}{
		{"partial line grouped over pages", func(t *testing.T, d string) {
			edit(t, d, "layouts/partials/card.html", "<p>{{ .Summary }}</p>", "<p>{{ .Summary }}</p>\n  <a class=\"more\" href=\"/guider/lon/\">x</a>")
		}, []string{
			"layouts/partials/card.html:5: broken link /guider/lon/: no page, file or endpoint at /guider/lon/ (did you mean /guider/lonn/?)",
			"      | <a class=\"more\" href=\"/guider/lon/\">x</a>",
			"      on 3 pages: /, /en/guides/, /guider/",
		}},
		{"static text inside a long text node", func(t *testing.T, d string) {
			edit(t, d, "layouts/base.html", "<main class=\"main\">", "<main class=\"main\">\n    <img src=\"/favicon.svg\">")
		}, []string{"layouts/base.html:15: <img> without alt attribute", "on 11 pages"}},
		{"markdown line", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Lønn **kjøres")
		}, []string{"content/nb/guider/lonn/index.md:8: visible markup \"**\""}},
		{"markdown link line", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "Lønn kjøres", "Lønn\n\nSe [x](/guider/lon/ \"x\") kjøres")
		}, []string{"content/nb/guider/lonn/index.md:10: broken link \"/guider/lon/\": no page, file or endpoint at /guider/lon/ (did you mean /guider/lonn/?)"}},
		{"front matter value", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "title: Lønn for nybegynnere", "title: Lønn **for** nybegynnere")
		}, []string{"content/nb/guider/lonn/index.md:2: visible markup \"**\""}},
		{"duplicate title", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "title: Lønn for nybegynnere", "title: Slik fungerer mva")
		}, []string{"content/nb/guider/mva/index.md:2: duplicate <title> \"Slik fungerer mva\" (also on content/nb/guider/lonn/index.md); make seoTitle or title unique"}},
		{"fragment suggestion", func(t *testing.T, d string) {
			edit(t, d, "content/nb/guider/lonn/index.md", "/guider/mva/#satser", "/guider/mva/#satsr")
		}, []string{"content/nb/guider/lonn/index.md:8: link /guider/mva/#satsr points to a missing #satsr on /guider/mva/ (did you mean \"satser\"?)"}},
		{"css line", func(t *testing.T, d string) {
			edit(t, d, "assets/site.css", "body {", ".ghost { color: red; }\nbody {")
		}, []string{"assets/site.css:2: CSS class .ghost is never used"}},
		{"missing map key", func(t *testing.T, d string) {
			edit(t, d, "layouts/about.html", "<h1>", `<p>{{ site.Params.contact.emial }}</p><h1>`)
		}, []string{`layouts/about.html:2:6: nothing to print (the value is missing); the last missing key was .emial in params.contact (did you mean "email"?)`, "on 2 pages: /en/about/, /om/"}},
		{"layout name", func(t *testing.T, d string) {
			edit(t, d, "content/nb/om/index.md", "layout: about", "layout: abuot")
		}, []string{`content/nb/om/index.md:4: layout "abuot" does not exist (layouts/abuot.html) (did you mean "about"?)`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := site(t)
			c.mut(t, d)
			err := buildErr(d)
			if err == nil {
				t.Fatal("build succeeded")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("got:\n%v\nwant a line containing %q", err, w)
				}
			}
		})
	}
}
