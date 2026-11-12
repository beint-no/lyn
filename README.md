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
lyn inspect [dir]   print the configuration (baseURL, title, endpoints, params, languages) and every page as JSON for tools
```

Install with `go install github.com/beint-no/lyn/cmd/lyn@latest`. A repository can pin a
version in a `.lyn-version` file (e.g. `v1.3.1`) in the site folder or any parent; a
released lyn of another version then refuses to run and prints the install command.

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
assets/**                   fingerprinted files served under /_assets/ ({{ asset "site.css" }}); CSS and JS minified
static/                     copied verbatim
static/favicon.svg          required; lyn renders /apple-touch-icon.png, /favicon.ico and /og-image.png from it
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
externalHosts: [www.youtube-nocookie.com]
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
path), which must exist and be claimed once. Template text that differs per
language belongs in `i18n/`; a single-language site has no `i18n/` and writes
its text in templates (or `params` when several templates share it). `endpoints` lists paths served by the Worker (form
handlers) so links to them are valid. Links to other lyn sites in the same repository (any folder with a `site.yml`
below the nearest `.git`, matched by `baseURL`) are checked against that site's
pages, bundle files, static files, generated files, endpoints and redirects at build
time, with no configuration; `www.` links to them fail too. `externalHosts` lists
every other host the pages load from or post to (scripts, stylesheets, images,
frames, media, preloads, form targets and CSS `url()`); loading from a host that is
not listed fails, as does a listed host nothing uses. Plain links need no entry.

Front matter keys: `title` and `description` (required), `seoTitle`,
`summary`, `layout`, `date`, `lastmod` (`YYYY-MM-DD` or RFC 3339), `weight` and
`noindex: true`. Site-specific keys are declared with a type (`string`, `bool`,
`int`, `list`, `map`) in `site.yml`: under `pageParams` when templates read them
(`.Params.key`), or under `toolParams` when only external tools read them (via
`lyn inspect`; templates cannot). Unknown keys, wrong types, values no template
reads and declarations no page uses all fail.
Folder and file names (content, `static/`, `assets/`) are lowercase words joined by hyphens; the folder path is the URL. Raster images are AVIF; other raster formats fail the build.

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
Layout Section CurrentSection Path Name File Site NoIndex Card`, methods `GetPage Param
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

`.Card` is the page's generated 1200×630 SVG card (title and description,
wrapped with Inter's metrics, long text ellipsized), served at `<page URL>card.svg`
and written only when a template renders it, so nothing generated lives in the
repository. It is a resource like any other (`RelPermalink`, `Width`, `Height`).

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
invalid UTF-8, a page body that the layout never renders, redundant values
(`seoTitle` equal to `title`, a page's `lastmod` equal to its `date`), `i18n/`
on a single-language site, a `date` or `lastmod` in the future and a `lastmod`
before `date`.

Links: indexable pages no other indexable page links to (orphans; noindex pages such as ad landings are exempt), broken internal links and fragments (in Markdown, templates, meta tags
and CSS `url()`), missing trailing slashes, links through a `_redirects`
rule, redirects whose source still exists or whose target is missing,
`http://` and protocol-relative links, `tel:` links that are not
`+<country code><digits>`, `mailto:` links with an invalid address or an
unknown, unencoded or `+`-spaced field (`urlquery` encodes spaces as `%20`),
mistyped schemes (`htps://`, `https//`, `www.` without one), external URLs
with spaces, credentials or an invalid host, `href="#"`, redirect chains and
loops in `_redirects`, more `_redirects` rules than Cloudflare accepts (2,000
static, 100 dynamic, where every rule from the first splat or placeholder on
counts as dynamic), `_redirects` rules whose source is not a `/` path, whose
target is not a `/` path or `https://` URL, that have no status (Cloudflare would
answer a temporary 302) or a status other than 200, 301, 302, 303, 307 or 308, or
that are longer than 1,000 characters, an indexable page without a canonical link, a
canonical link or `og:url` that is not the page's own URL, a canonical link on
the 404 page, and `hreflang` alternates on this domain that are not the page or
one of its translations (or that leave out the page itself).

HTML: unbalanced tags, duplicate ids, missing or duplicate `<title>`, missing
or duplicate meta description on indexable pages, indexable pages without
exactly one `<h1>`, `noindex` pages without the robots meta, `<img>` without
`alt`, `<a>` and `<button>` without accessible text (text, `title`, image
`alt` or svg `<title>`; empty elements that scripts fill are allowed when they
have an `id`, `data-*` or `hidden`), empty headings, `<iframe>` without
`title`, `<label for>` without its target, JSON-LD that is invalid, lacks
`"@context": "https://schema.org"` or has empty values, inline `<style>`,
visible Markdown or shortcode syntax.

HTML structure: a page without `<!doctype html>`, `<meta charset>`,
`<meta name="viewport">`, `<html lang>` or exactly one `<main>` (`hidden` ones
aside); an `<html lang>` that is neither the page language's code nor its locale;
`<input>`, `<select>` and `<textarea>` without a label (`<label for>`, a wrapping
`<label>` or a `title`); indexable pages without `og:title`, `og:description`,
`og:image`, `og:url`, `og:type` or `twitter:card`; a page over 500 KB of HTML; a viewport that stops zooming (`user-scalable=no`, `maximum-scale` below
5); `<script src>` without `defer`, `async` or `type="module"`; markup that browsers silently
rearrange (block elements inside `<p>` or text-level elements, links or
controls inside `<a>`/`<button>`, `<form>` inside `<form>`, `<li>`, `<dt>`,
`<dd>`, table parts, `<option>`, `<summary>`, `<legend>`, `<figcaption>`,
`<source>` and `<track>` outside their parent, other elements or text directly
inside lists, tables and rows, `<meta>`, `<title>`, `<style>`, `<base>` and
non-resource `<link>` outside `<head>`); unknown elements (custom elements need
a hyphen) and unknown or duplicate attributes, checked per element outside
`<svg>` and `<math>`; invalid enumerated values (`type`, `method`,
`enctype`, `rel`, `target`, `loading`, `decoding`, `fetchpriority`,
`autocomplete`, `inputmode`, `enterkeyhint`, `crossorigin`,
`referrerpolicy`, `popover`, `dir`, `translate`, `spellcheck`, `hidden` and
more); boolean attributes with a value such as `required="false"`; `id`s with
spaces; `lang`/`hreflang` that are not language tags; positive `tabindex`;
`width`/`height` with units; `popovertarget`, `commandfor`, `list`, `form`
and `aria-*` references to missing ids; `<img>` without `width` and `height`
or whose declared size has other proportions than the local file (±1 px);
invalid `srcset`/`sizes`; duplicate `<meta>` names; relative `og:image`/
`og:url`; skipped heading levels inside `<main>`; inline scripts with syntax
errors.

Text: garbled encoding (`Ã¸`, `â€™`, `�`) outside code, placeholder text
(`lorem ipsum`, `TODO`, `FIXME`, `TBD`, `[insert`), literal entities such as
a visible `&nbsp;`, unrendered Markdown (table rows, `[^1]` footnotes,
`[text][ref]` links, `## ` headings) in page text and in `alt`, `title`,
`aria-label` and description meta tags.

Files: SVG files in `static/`, bundles and `assets/` must be well-formed XML
(balanced tags, quoted attributes, escaped `&` and `<`) with an `<svg>` root
that has a `viewBox`, and no `<script>`, event handlers or external
references. Scripts in `static/` must parse. CSS must use known properties,
descriptors, at-rules and pseudo-classes and valid keywords for common
properties (`display: blokc` fails with a suggestion), and must not declare the
same property and value twice in one rule. AVIF and SVG files over 500 KB and CSS and
JS files over 150 KB (after minification for `assets/`) fail. `static/_headers` is required, its `/*` rule must set
`X-Content-Type-Options: nosniff`, and its rules must be a path followed by indented
`Name: value` lines and must match at least one built file. More than 50,000 indexable
pages (the sitemap limit) fail. Translations must
use the same format verbs in every language, and `T` must pass a value exactly
when the translation has one.

Dead code: CSS classes nothing renders, classes with no CSS rule, CSS `#id`
selectors and script `getElementById`/`querySelector("#id")` lookups for ids
no page renders, custom
properties nothing reads, `var()` without fallback for an undefined property,
`data-*` attributes nothing reads, unused translations, params, data keys and
front matter params, menus no template renders, unused layouts, partials, shortcodes, assets, bundle
files and linked-file types in `static/`.

## Error messages

Every problem is one line `file:line: what is wrong; what to do`, sorted by
file and line, followed by the source line and, when the file is a template,
the pages it affects:

```
layouts/partials/card.html:4: broken link /guider/lon/: no page, file or endpoint at /guider/lon/ (did you mean /guider/lonn/?)
      | <a class="more" href="/guider/lon/">x</a>
      on 2 pages: /, /guider/
```

Problems found in rendered HTML point to the bytes' source: the template line
for static markup and actions (pages are re-rendered with source tracking only
when a problem exists, so passing builds pay nothing), the front matter line
for titles, descriptions and summaries, and the Markdown line for page content.
The same problem from one template line is reported once with the number of
pages and a few examples. Failed name lookups (fields, functions, partials,
layouts, shortcodes, assets, translations, front matter keys, map keys, CSS
classes, ids and link targets) suggest the nearest existing name.

## Headers

Every fingerprinted asset is served under `/_assets/`, and lyn writes
`public/_headers` as `static/_headers` plus one rule caching `/_assets/*` for a
year as immutable. Rules in `static/_headers` that match no output file fail the
build, as does a rule for `/_assets/`.

## Cloudflare Worker configuration

When the site folder has a `wrangler.jsonc`, lyn checks it: it must be plain JSON
(no comments or trailing commas) with `"workers_dev": false`, `"preview_urls": false`,
`assets.directory` `"./public"`, `assets.not_found_handling` `"404-page"` and a route
for the `baseURL` host. `endpoints` need a `main` Worker, and unless
`assets.run_worker_first` is `true` it must cover every endpoint and every `/api/`
path the Worker scripts compare `pathname` with; an uncovered path would get the 404
page (or 405 for a form post) without reaching the Worker.

## Generated images

Icons, the share image and cards are rendered by lyn itself. Rendered images are
also kept in the user cache directory (`lyn/` under the OS cache folder), keyed by
their exact inputs and the lyn binary, so a clean build of an unchanged site does
not render them again.

## Icons

`static/favicon.svg` is the only icon source. lyn renders `/apple-touch-icon.png`
(180×180, opaque: the square is filled with the first shape's stroke or fill so iOS
can round it) and `/favicon.ico` (32×32) from it with its own rasterizer, so no
raster icons live in the repository. Icons use a small SVG subset: a square
`viewBox`, `rect`, `circle`, `path` (all commands), `g`, `linearGradient`, solid
`#rgb`/`#rrggbb` colours, stroke width, caps and joins, and `matrix()`
transforms. Anything else (such as `<text>`; draw letters as paths) fails the build.

## Social share image

lyn renders the site's 1200×630 `/og-image.png` on every build, so no share image
lives in the repository and it never goes stale. It shows the `baseURL` host in
the accent colour, the brand in large bold type, the first language's home page
`summary` (as plain text; its `description` when there is no summary) in semibold,
wrapped to five lines and ellipsized after that, `static/favicon.svg` at the right
with a soft shadow, and an accent bar along the bottom. The brand is the site
`title` up to its first ` | `, ` – `, ` — ` or ` - `, so `Beint | Kunnskap og
deltakelse` shows `Beint`; it shrinks only when it does not fit on one line. The
accent is the favicon's most saturated colour that reads on white (fills, strokes
and gradient stops); the bar uses it as drawn, the background is it tinted towards
white, and the host is darkened until it has 4.5:1 contrast. Text is set in Inter
(embedded, SIL Open Font License) with its kerning; a character Inter's Latin
subset lacks fails the build at the title or summary line. Link it with
`{{ absURL "/og-image.png" }}`. A `static/og-image.png` fails the build. The image
is re-rendered only when the host, title, summary or favicon changes.

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
3.9 s instead of 32.4 s (8.3×), no-change rebuilds 1.1 s (29×), a one-line content edit
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
