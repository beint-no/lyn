package gen

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

var words = strings.Fields(`regnskap bedrift faktura bilag skatt avgift lønn ansatt kunde leverandør bank konto
budsjett resultat balanse aksjeselskap enkeltpersonforetak frist rapport årsoppgjør revisor kostnad inntekt
salg kjøp betaling purring kreditt debet periode måned kvartal oversikt system digital automatisk enkel trygg
rask viktig ofte gjelder alltid noen mange flere første siste store små norske kommune stat regel lov forskrift`)

type Page struct {
	Section, Slug, Title string
	Weight               int
	Shortcode            bool
	Links                []int
}

type Site struct {
	N        int
	Sections []string
	Pages    []Page
}

func sentence(r *rand.Rand, n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = words[r.IntN(len(words))]
	}
	s := strings.Join(w, " ")
	r0 := []rune(s)
	return strings.ToUpper(string(r0[:1])) + string(r0[1:]) + "."
}

func Plan(n int) *Site {
	r := rand.New(rand.NewPCG(uint64(n), 7))
	s := &Site{N: n}
	nsec := max(2, min(20, n/50))
	for i := range nsec {
		s.Sections = append(s.Sections, fmt.Sprintf("seksjon-%02d", i))
	}
	for i := range n {
		sec := s.Sections[i%nsec]
		p := Page{Section: sec, Slug: fmt.Sprintf("artikkel-%05d", i), Title: fmt.Sprintf("%s %d", strings.TrimSuffix(sentence(r, 4), "."), i), Weight: 1 + r.IntN(50), Shortcode: i%5 == 0}
		for range 5 {
			p.Links = append(p.Links, r.IntN(n))
		}
		s.Pages = append(s.Pages, p)
	}
	return s
}

func body(s *Site, i int, hugo bool) string {
	r := rand.New(rand.NewPCG(uint64(i), 11))
	p := s.Pages[i]
	var b strings.Builder
	for range 3 {
		b.WriteString(sentence(r, 30) + " ")
	}
	t := s.Pages[p.Links[0]]
	fmt.Fprintf(&b, "Les om [%s](/%s/%s/ \"%s\").\n\n", strings.ToLower(t.Title), t.Section, t.Slug, t.Title)
	b.WriteString("## Oversikt\n\n")
	for k := range 4 {
		b.WriteString(sentence(r, 25) + " ")
		if k < len(p.Links)-1 {
			t := s.Pages[p.Links[k+1]]
			fmt.Fprintf(&b, "Se også [%s](/%s/%s/#oversikt \"%s\"). ", strings.ToLower(t.Title), t.Section, t.Slug, t.Title)
		}
	}
	b.WriteString("\n\n![Illustrasjon](illustrasjon.svg)\n\n")
	b.WriteString("| Post | Beløp | Frist |\n|------|------:|-------|\n")
	for k := range 4 {
		fmt.Fprintf(&b, "| %s | %d kr | %d. mars |\n", words[r.IntN(len(words))], 100*(k+1)*r.IntN(90), 1+r.IntN(28))
	}
	b.WriteString("\n## Detaljer\n\n")
	for range 5 {
		fmt.Fprintf(&b, "- **%s**: %s\n", words[r.IntN(len(words))], sentence(r, 12))
	}
	b.WriteString("\n")
	if p.Shortcode {
		b.WriteString("{{< tips tittel=\"Husk\" >}}\n" + sentence(r, 15) + "\n{{< /tips >}}\n\n")
	}
	b.WriteString("### Oppsummering\n\n")
	for range 3 {
		b.WriteString(sentence(r, 35) + " ")
	}
	b.WriteString("\n")
	return b.String()
}

const favicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#1768ef"/><circle cx="32" cy="32" r="14" fill="#fff"/></svg>`

const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 640 320"><rect width="640" height="320" fill="#eef"/><circle cx="320" cy="160" r="90" fill="#1768ef"/></svg>`

func w(dir, rel, content string) {
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		panic(err)
	}
}

func front(title, desc, summary string, weight int) string {
	s := fmt.Sprintf("---\ntitle: %s\ndescription: %s\n", title, desc)
	if summary != "" {
		s += "summary: " + summary + "\n"
	}
	if weight > 0 {
		s += fmt.Sprintf("weight: %d\n", weight)
	}
	return s + "date: 2026-05-01\n---\n"
}

func content(s *Site, dir string, hugo bool) {
	r := rand.New(rand.NewPCG(3, 3))
	w(dir, "_index.md", front("Regnskap for små bedrifter", "Guider om regnskap, skatt og lønn for små bedrifter.", "", 0))
	for i, sec := range s.Sections {
		w(dir, sec+"/_index.md", front(fmt.Sprintf("Seksjon %d om %s", i, words[i%len(words)]), fmt.Sprintf("Artikler i seksjon %d med praktiske guider for små bedrifter.", i), fmt.Sprintf("Alt om %s.", words[i%len(words)]), i+1))
	}
	for i, p := range s.Pages {
		w(dir, p.Section+"/"+p.Slug+"/index.md", front(p.Title, sentence(r, 14), sentence(r, 18), p.Weight)+body(s, i, hugo))
		w(dir, p.Section+"/"+p.Slug+"/illustrasjon.svg", svg)
	}
}

const css = `:root { --accent: #1768ef; --gap: 1rem; --muted: #555; }
body { margin: 0; font-family: system-ui, sans-serif; line-height: 1.6; }
.header { display: flex; gap: var(--gap); padding: var(--gap); }
.brand { font-weight: 700; }
.nav { display: flex; gap: var(--gap); flex-wrap: wrap; }
.nav-link { color: var(--accent); }
.nav-link.is-active { text-decoration: underline; }
.crumbs { font-size: .875rem; color: var(--muted); }
.main { display: grid; grid-template-columns: minmax(0, 1fr); padding: var(--gap); }
.prose table { border-collapse: collapse; }
.table-scroll { overflow-x: auto; }
.toc { border-left: 2px solid var(--accent); padding-left: var(--gap); }
.related, .cards { display: grid; gap: var(--gap); grid-template-columns: repeat(auto-fill, minmax(16rem, 1fr)); }
.card { border: 1px solid #ddd; border-radius: .5rem; padding: var(--gap); }
.card-img { width: 100%; height: auto; }
.card-title { font-size: 1.125rem; }
.tips { border-left: 4px solid var(--accent); padding: var(--gap); }
.footer { padding: var(--gap); color: var(--muted); }
.footer-links { display: flex; gap: var(--gap); flex-wrap: wrap; }
.count { color: var(--muted); }
`

func GenerateLyn(dir string, n int) {
	s := Plan(n)
	content(s, filepath.Join(dir, "content", "nb"), false)
	var menu strings.Builder
	for i, sec := range s.Sections {
		fmt.Fprintf(&menu, "        - name: Seksjon %d\n          pageRef: /%s\n          weight: %d\n", i, sec, i+1)
	}
	w(dir, "site.yml", "baseURL: https://example.no/\ntitle: Benchmark\nlanguages:\n  - code: nb\n    locale: nb-NO\n    label: Norsk\n    menus:\n      main:\n"+menu.String()+"params:\n  company: Eksempel AS\n")
	w(dir, "assets/site.css", css)
	w(dir, "static/favicon.svg", favicon)
	w(dir, "static/_headers", "/*\n  X-Content-Type-Options: nosniff\n")
	w(dir, "layouts/base.html", `<!doctype html>
<html lang="{{ .Language.Code }}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ .SeoTitle }}</title>
<meta name="description" content="{{ .Description }}">
{{ if ne .Kind "404" }}<link rel="canonical" href="{{ .Permalink }}">{{ end }}
<meta property="og:title" content="{{ .Title }}">
<meta property="og:description" content="{{ .Description }}">
<meta property="og:type" content="website">
<meta property="og:image" content="{{ absURL "/og-image.png" }}">
<meta name="twitter:card" content="summary_large_image">
{{ if ne .Kind "404" }}<meta property="og:url" content="{{ .Permalink }}">{{ end }}
{{ with asset "site.css" }}<link rel="stylesheet" href="{{ .RelPermalink }}" integrity="{{ .Integrity }}">{{ end }}
<link rel="icon" href="/favicon.svg">
</head>
<body>
{{ partial "header" . }}
<main class="main">
{{ block "main" . }}{{ end }}
</main>
{{ partial "footer" . }}
</body>
</html>
`)
	w(dir, "layouts/partials/header.html", `<header class="header">
<a class="brand" href="/">{{ site.Title }}</a>
<nav class="nav">
{{- range site.Menus.main }}
<a class="nav-link{{ if hasPrefix page.RelPermalink .URL }} is-active{{ end }}" href="{{ .URL }}">{{ .Name }}</a>
{{- end }}
</nav>
</header>
`)
	w(dir, "layouts/partials/crumbs.html", `<nav class="crumbs">
{{- range reverse .Ancestors }}<a href="{{ .RelPermalink }}">{{ .Title }}</a> / {{ end -}}
<span>{{ .Title }}</span>
</nav>
`)
	w(dir, "layouts/partials/footer.html", `<footer class="footer">
<div class="footer-links">
{{- range site.Home.Sections }}<a href="{{ .RelPermalink }}">{{ .Title }}</a>{{ end -}}
</div>
<p>© {{ site.Params.company }}</p>
</footer>
`)
	w(dir, "layouts/partials/card.html", `<article class="card">
{{- with .Resources.Get "illustrasjon.svg" }}<img class="card-img" src="{{ .RelPermalink }}" alt="" width="{{ .Width }}" height="{{ .Height }}" loading="lazy">{{ end -}}
<h2 class="card-title"><a href="{{ .RelPermalink }}">{{ .Title }}</a></h2>
<p>{{ .Summary }}</p>
</article>
`)
	w(dir, "layouts/page.html", `{{ define "main" }}
{{ partial "crumbs" . }}
<article class="prose">
<h1>{{ .Title }}</h1>
<nav class="toc"><h2>Innhold</h2>{{ .TableOfContents }}</nav>
{{ .Content }}
</article>
<h2>Relaterte artikler</h2>
<div class="related">
{{- $self := . -}}
{{- range first 6 (where .Parent.RegularPages "Path" "!=" .Path) }}{{ partial "card" . }}{{ end -}}
</div>
{{ end }}
`)
	w(dir, "layouts/section.html", `{{ define "main" }}
{{ partial "crumbs" . }}
<h1>{{ .Title }}</h1>
<p>{{ .Summary }}</p>
<div class="cards">
{{- range .RegularPages }}{{ partial "card" . }}{{ end -}}
</div>
{{ end }}
`)
	w(dir, "layouts/home.html", `{{ define "main" }}
<h1>{{ .Title }}</h1>
{{- range .Sections }}
<section>
<h2><a href="{{ .RelPermalink }}">{{ .Title }}</a> <span class="count">{{ len .RegularPages }} artikler</span></h2>
<div class="cards">{{ range first 3 .RegularPages }}{{ partial "card" . }}{{ end }}</div>
<a href="{{ .RelPermalink }}">Les mer</a>
</section>
{{- end }}
{{ end }}
`)
	w(dir, "layouts/404.html", `{{ define "main" }}<h1>Fant ikke siden</h1><a href="/">Hjem</a>{{ end }}
`)
	w(dir, "layouts/shortcodes/tips.html", `<aside class="tips"><strong>{{ .Get "tittel" }}</strong>{{ .Inner }}</aside>
`)
}

func GenerateHugo(dir string, n int, hooks bool) {
	s := Plan(n)
	content(s, filepath.Join(dir, "content"), true)
	var menu strings.Builder
	for i, sec := range s.Sections {
		fmt.Fprintf(&menu, "    - name: Seksjon %d\n      pageRef: /%s\n      weight: %d\n", i, sec, i+1)
	}
	w(dir, "hugo.yml", `baseURL: https://example.no/
title: Benchmark
locale: nb-NO
disableKinds: ["taxonomy", "term", "rss"]
enableRobotsTXT: true
disableHugoGeneratorInject: true
markup:
  goldmark:
    renderer:
      unsafe: true
    parser:
      attribute:
        block: true
minify:
  minifyOutput: true
params:
  company: Eksempel AS
menus:
  main:
`+menu.String())
	w(dir, "i18n/nb.yml", "read_more: Les mer\ncontents: Innhold\nrelated: Relaterte artikler\narticles: artikler\nhome: Hjem\nnot_found: Fant ikke siden\n")
	w(dir, "assets/site.css", css)
	w(dir, "static/favicon.svg", favicon)
	w(dir, "layouts/baseof.html", `<!doctype html>
<html lang="{{ .Language.Lang }}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ .Params.seoTitle | default .Title }}</title>
<meta name="description" content="{{ .Description }}">
{{ if ne .Kind "404" }}<link rel="canonical" href="{{ .Permalink }}">{{ end }}
<meta property="og:title" content="{{ .Title }}">
{{ if ne .Kind "404" }}<meta property="og:url" content="{{ .Permalink }}">{{ end }}
{{ with resources.Get "site.css" | minify | fingerprint }}<link rel="stylesheet" href="{{ .RelPermalink }}" integrity="{{ .Data.Integrity }}">{{ end }}
<link rel="icon" href="/favicon.svg">
</head>
<body>
{{ partial "header.html" . }}
<main class="main">
{{ block "main" . }}{{ end }}
</main>
{{ partial "footer.html" . }}
</body>
</html>
`)
	w(dir, "layouts/_partials/header.html", `<header class="header">
<a class="brand" href="/">{{ site.Title }}</a>
<nav class="nav">
{{- range site.Menus.main }}
<a class="nav-link{{ if hasPrefix page.RelPermalink .URL }} is-active{{ end }}" href="{{ .URL }}">{{ .Name }}</a>
{{- end }}
</nav>
</header>
`)
	w(dir, "layouts/_partials/crumbs.html", `<nav class="crumbs">
{{- range .Ancestors.Reverse }}<a href="{{ .RelPermalink }}">{{ .Title }}</a> / {{ end -}}
<span>{{ .Title }}</span>
</nav>
`)
	w(dir, "layouts/_partials/footer.html", `<footer class="footer">
<div class="footer-links">
{{- range site.Home.Sections }}<a href="{{ .RelPermalink }}">{{ .Title }}</a>{{ end -}}
</div>
<p>© {{ site.Params.company }}</p>
</footer>
`)
	w(dir, "layouts/_partials/card.html", `<article class="card">
{{- with .Resources.Get "illustrasjon.svg" }}{{ $size := findRESubmatch `+"`"+`viewBox="0 0 (\d+) (\d+)"`+"`"+` .Content 1 }}<img class="card-img" src="{{ .RelPermalink }}" alt="" width="{{ index $size 0 1 }}" height="{{ index $size 0 2 }}" loading="lazy">{{ end -}}
<h2 class="card-title"><a href="{{ .RelPermalink }}">{{ .Title }}</a></h2>
<p>{{ .Summary }}</p>
</article>
`)
	w(dir, "layouts/single.html", `{{ define "main" }}
{{ partial "crumbs.html" . }}
<article class="prose">
<h1>{{ .Title }}</h1>
<nav class="toc"><h2>{{ T "contents" }}</h2>{{ .TableOfContents }}</nav>
{{ .Content }}
</article>
<h2>{{ T "related" }}</h2>
<div class="related">
{{- range first 6 (where .Parent.RegularPages "Path" "!=" .Path) }}{{ partial "card.html" . }}{{ end -}}
</div>
{{ end }}
`)
	w(dir, "layouts/section.html", `{{ define "main" }}
{{ partial "crumbs.html" . }}
<h1>{{ .Title }}</h1>
<p>{{ .Summary }}</p>
<div class="cards">
{{- range .RegularPages }}{{ partial "card.html" . }}{{ end -}}
</div>
{{ end }}
`)
	w(dir, "layouts/home.html", `{{ define "main" }}
<h1>{{ .Title }}</h1>
{{- range .Sections }}
<section>
<h2><a href="{{ .RelPermalink }}">{{ .Title }}</a> <span class="count">{{ len .RegularPages }} {{ T "articles" }}</span></h2>
<div class="cards">{{ range first 3 .RegularPages }}{{ partial "card.html" . }}{{ end }}</div>
<a href="{{ .RelPermalink }}">{{ T "read_more" }}</a>
</section>
{{- end }}
{{ end }}
`)
	w(dir, "layouts/404.html", `{{ define "main" }}<h1>{{ T "not_found" }}</h1><a href="/">{{ T "home" }}</a>{{ end }}
`)
	w(dir, "layouts/_shortcodes/tips.html", `<aside class="tips"><strong>{{ .Get "tittel" }}</strong>{{ .Inner | .Page.RenderString }}</aside>
`)
	if !hooks {
		return
	}
	w(dir, "layouts/_markup/render-link.html", `{{- $u := urls.Parse .Destination -}}
{{- $href := .Destination -}}
{{- if and (eq $u.Scheme "") (ne $u.Path "") -}}
  {{- $target := site.GetPage $u.Path | default (.Page.GetPage $u.Path) -}}
  {{- if not $target -}}{{- errorf "Broken internal link %q on %s" .Destination .Page.File.Path -}}{{- end -}}
  {{- if $u.Fragment -}}
    {{- if not ($target.Fragments.Identifiers.Contains $u.Fragment) -}}{{- errorf "Broken fragment %q on %s" .Destination .Page.File.Path -}}{{- end -}}
    {{- $href = printf "%s#%s" $target.RelPermalink $u.Fragment -}}
  {{- else -}}
    {{- $href = $target.RelPermalink -}}
  {{- end -}}
{{- end -}}
<a href="{{ $href | safeURL }}"{{ with .Title }} title="{{ . }}"{{ end }}>{{ .Text }}</a>
{{- /**/ -}}
`)
	w(dir, "layouts/_markup/render-image.html", `{{- $img := .Page.Resources.Get .Destination -}}
{{- if not $img -}}{{- errorf "Broken image %q on %s" .Destination .Page.File.Path -}}{{- end -}}
{{- $size := "" -}}
{{- with findRESubmatch `+"`"+`viewBox="0 0 (\d+) (\d+)"`+"`"+` $img.Content 1 -}}{{- $size = printf " width=\"%s\" height=\"%s\"" (index . 0 1) (index . 0 2) -}}{{- end -}}
<img src="{{ $img.RelPermalink }}" alt="{{ .Text }}"{{ $size | safeHTMLAttr }}{{ if .Ordinal }} loading="lazy" decoding="async"{{ end }}>
{{- /**/ -}}
`)
}
