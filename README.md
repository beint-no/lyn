# lyn

An opinionated static site generator built for speed and for agents. It builds a site into
`public/` and **fails the build on anything broken or unused**: broken links and
fragments, missing images, dead CSS, unstyled classes, unused translations,
unused params and front matter, unused partials, layouts, assets and files,
unsafe template output, invalid HTML structure and more. There is one way to do
each thing and no configuration beyond `site.yml`.

```
lyn build [dir]     build once (incremental when .lyn/state exists)
lyn serve [dir]     build, serve on http://localhost:1313, rebuild on change
lyn inspect [dir]   print the configuration and every page as JSON for tools
```

Install with `go install github.com/beint-no/lyn/cmd/lyn@latest`.

## Site layout

```
site.yml                    the only configuration file
content/<lang>/_index.md    home page; every folder with pages needs _index.md
content/<lang>/a/_index.md  section /a/
content/<lang>/a/b/index.md page /a/b/, plus its bundle files (b/*.svg, ...)
layouts/base.html           base document with {{ block "name" . }} slots
layouts/<layout>.html       home, section, page, 404, or a front matter layout
layouts/partials/*.html     partials ({{ partial "name" ctx }})
layouts/shortcodes/*.html   shortcodes ({{< name key="value" >}})
i18n/<lang>.yml             translations (snake_case keys, all languages)
data/**/*.json              data (site.Data.<folder>.<file>), strict JSON, no null
assets/**                   fingerprinted files ({{ asset "site.css" }}); CSS and JS minified
static/                     copied verbatim
```

`public/` and `.lyn/` are build output; never edit them.

## Configuration and data format

`site.yml`, front matter and `i18n/` use one strict YAML subset (data files are JSON):
block maps and lists, double-quoted strings, plain strings, `|`/`>` blocks and
one-line `[a, b]` lists. Lines starting with `#` are allowed only inside block
scalars. Only `true`, `false`, canonical integers and decimals
are typed; everything else is a string, so `NO` and `0150` stay strings. No
comments, null, anchors, tags, single quotes or duplicate keys. Errors carry
file and line.

```yaml
baseURL: https://example.no/
title: Example
endpoints: ["/submit"]
siblings:
  - baseURL: https://sister.example/
    root: ../sister-site
languages:
  - code: nb
    locale: nb-NO
    label: Norsk
    menus:
      main:
        - name: Guides
          pageRef: /guider
          weight: 1
params:
  contact:
    email: post@example.no
```

The first language is served at `/`, others at `/<code>/`. Pages with the same
path in different languages are translations of each other; a page whose
translated path differs declares `translationOf: /path/` (the first language's
path), which must exist and be claimed once. Language-specific
text belongs in `i18n/`. `endpoints` lists paths served by the Worker (form
handlers) so links to them are valid. `siblings` names sister sites on other
domains with their local folder: every link to them is checked against their
pages and static files at build time.

Front matter keys: `title` and `description` (required), `seoTitle`,
`summary`, `layout`, `date`, `lastmod` (`YYYY-MM-DD` or RFC 3339), `weight` and
`noindex: true`. Site-specific keys are declared with a type (`string`, `bool`,
`int`, `list`, `map`) in `site.yml`: under `pageParams` when templates read them
(`.Params.key`), or under `toolParams` when only external tools read them (via
`lyn inspect`; templates cannot). Unknown keys, wrong types, values no template
reads and declarations no page uses all fail.
Folder and file names are lowercase ASCII; the folder path is the URL.

## Templates

Go template syntax, compiled once into closures with a contextual HTML escaper
and statically checked names. Template comments are not allowed.

- Escaping is decided at compile time from the HTML context: text, quoted
  attribute, URL attribute (unsafe schemes fail), `style` attribute (CSS-safe
  values only), inside a tag (only `attr` values), `<script>` (only `json`
  output). Unquoted attributes, inline event handlers and output inside HTML
  comments fail to compile.
- Static text is minified at compile time: comments are stripped and
  whitespace between block-level tags removed, so there is no runtime HTML
  minifier.
- Partials are pure. Calls are memoized automatically per argument value
  (adaptively disabled for partials that never repeat), so there is no
  `partialCached`. A partial either outputs markup or ends with
  `{{ return value }}`.
- Uppercase names are fields (`.Title`), lowercase names are map keys
  (`.Params.badge`). An unknown field fails at compile time; printing a
  missing value fails at render time.

Page fields: `Title SeoTitle Description Summary Content TableOfContents
WordCount ReadingTime RawContent RelPermalink Permalink Kind IsHome IsSection IsPage
Parent Ancestors Pages RegularPages RegularPagesRecursive Sections Resources
Params Language Translations AllTranslations IsTranslated Date Lastmod Weight
Layout Section CurrentSection Path Name File Site NoIndex`, methods `GetPage Param
HasFragment IsAncestor IsDescendant`.

Site: `Title BaseURL Params Menus Home Pages RegularPages Sections Language
Languages Data GetPage`. Menu entry: `Name Title URL Page Weight Params
Children HasChildren`. Resource: `Name RelPermalink Permalink Integrity
MediaType Content Width Height` (SVG, PNG, JPEG, GIF, WebP and AVIF sizes);
resource lists: `Get Match`. `asset` takes a literal name (checked at compile
time) or any expression (checked when rendered). Language: `Code
Locale Label Title Weight Home`. Time: `Format Year IsZero`. Shortcode: `Get
Inner Page Params`.

Functions: `partial site page T asset link absURL year fail`, logic `and or
not eq ne lt le gt ge default cond`, collections `dict list append merge index
first last after in where sort uniq reverse seq len`, strings `print printf
lower upper trim trimSpace trimPrefix trimSuffix hasPrefix hasSuffix contains
replace replaceRE findRE findRESubmatch htmlUnescape split join truncate plainify markdownify slug
urlquery`, numbers `int float string add sub mul div mod round floor ceil`,
output `safeHTML attr json`.

`T` takes literal keys, checked at compile time. `.Summary` is inline Markdown.
`truncate` cuts at a word boundary and appends " …"; `WordCount` counts the
rendered content. `link "/path"`
returns the canonical URL of a page or file and fails if it does not exist.

## Markdown

Goldmark with GFM, definition lists, footnotes, typographer, `{.class}`
attributes and raw HTML. Relative and absolute links are resolved to the
target page's canonical URL; links to missing pages, `http://` links and
links to this site's own domain fail. Images resolve against the page bundle
or `static/`, get `width`/`height` from the file, and every image after the
first is lazy-loaded. Tables are wrapped in `<div class="table-scroll">` and code blocks get
`tabindex="0"` so scrollable content is keyboard reachable.
Headings get GitHub-style ids from their visible text (lowercase, letters,
digits, `-` and `_` kept, each space becomes `-`); `{#id}` sets one explicitly.
Shortcode inner content is Markdown.

## What fails the build

Content and config: invalid YAML, unknown keys, missing title or description,
non-ASCII or nested-without-`_index.md` folders, standalone `.md` files,
invalid UTF-8, a page body that the layout never renders.

Links: broken internal links and fragments (in Markdown, templates, meta tags
and CSS `url()`), missing trailing slashes, links through a `_redirects`
rule, redirects whose source still exists or whose target is missing,
`http://` and protocol-relative links.

HTML: unbalanced tags, duplicate ids, missing or duplicate `<title>`, missing
meta description on indexable pages, `noindex` pages without the robots meta,
`<img>` without `alt`, inline `<style>`, visible Markdown or shortcode syntax.

Dead code: CSS classes nothing renders, classes with no CSS rule, custom
properties nothing reads, `var()` without fallback for an undefined property,
`data-*` attributes nothing reads, unused translations, params, data keys and
front matter params, unused layouts, partials, shortcodes, assets, bundle
files and linked-file types in `static/`.

## Incremental builds

Every rendered page records exactly what it read: other pages' metadata or
bodies, section listings, path lookups, translations, map keys, assets and
template files (including reads inside memoized partials). The next build
hashes inputs, re-renders only pages whose recorded inputs changed, rewrites
only changed files and re-runs all site-wide checks on the stored records, so
an incremental build reports exactly the errors a cold build would.
Content folders and `index.md` files whose size, modification time and inode
are unchanged are not read again, and minified CSS and JS are reused
(`.lyn/content`); state and cache changes are appended rather than rewritten.
`lyn serve` additionally reuses its content scan and re-reads only the files
the watcher reports. A randomized test applies hundreds of edits (bodies,
titles, weights, added and removed pages, templates, CSS, i18n, config,
images, deleted output) and checks that incremental output is byte-identical
to a cold build.

## Results

lyn builds all 72 sites of a production marketing repository (32,961 pages,
previously Hugo with link-checking render hooks). Cold builds of the whole fleet take
4.2 s instead of 32.4 s (7.7×), no-change rebuilds 1.1 s (29×), a one-line content edit
in every site 1.2 s, and single edits in `lyn serve` re-render only the affected pages
(3–90 ms on 100–10,000 pages, 6–25× faster than `hugo server`). Every check that used
to run as separate scripts after Hugo now runs inside the build. The migration surfaced
real defects on the live sites: auto-generated section pages with broken titles and no description, layouts
that did not exist (silently falling back), stray tool output and raw LaTeX in published
articles, empty company details and labels, and thousands of
unused front matter values, parameters, translations and CSS rules.

## Development

```
go test ./...
go test ./bench -bench . -run x
go run ./bench/compare -sizes 100,1000,5000 -lyn $(which lyn)
```

`bench/compare` generates the same site for Hugo (with link and image check
hooks like `themes/common`) and lyn, then compares cold builds, no-change
rebuilds and incremental edits (Hugo via `hugo server`).
