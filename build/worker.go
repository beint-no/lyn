package build

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	maxImageBytes = 500 << 10
	maxCodeBytes  = 150 << 10
)

var workerRouteRE = regexp.MustCompile(`pathname\s*===\s*["'](/api/[^"']*)["']|pathname\.startsWith\(\s*["'](/api/[^"']*)["']`)

type wranglerConfig struct {
	Main        string `json:"main"`
	WorkersDev  *bool  `json:"workers_dev"`
	PreviewURLs *bool  `json:"preview_urls"`
	Routes      []struct {
		Pattern string `json:"pattern"`
	} `json:"routes"`
	Assets struct {
		Directory      string          `json:"directory"`
		NotFound       string          `json:"not_found_handling"`
		RunWorkerFirst json.RawMessage `json:"run_worker_first"`
	} `json:"assets"`
}

func (p *Project) checkWorker() {
	const file = "wrangler.jsonc"
	raw, err := os.ReadFile(filepath.Join(p.Root, file))
	if err != nil {
		return
	}
	var cfg wranglerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		p.errs.add("%s: not valid JSON (comments and trailing commas are not allowed): %v", file, err)
		return
	}
	if cfg.WorkersDev == nil || *cfg.WorkersDev {
		p.errs.add(`%s: set "workers_dev": false; the site is served only on its own domain`, file)
	}
	if cfg.PreviewURLs == nil || *cfg.PreviewURLs {
		p.errs.add(`%s: set "preview_urls": false; preview URLs publish copies of the site on another domain`, file)
	}
	if cfg.Assets.Directory != "./public" {
		p.errs.add(`%s: assets.directory must be "./public", where lyn writes the site`, file)
	}
	if cfg.Assets.NotFound != "404-page" {
		p.errs.add(`%s: assets.not_found_handling must be "404-page" so unknown URLs get 404.html with status 404`, file)
	}
	host := strings.TrimPrefix(p.BaseURL, "https://")
	if !slices.ContainsFunc(cfg.Routes, func(r struct {
		Pattern string `json:"pattern"`
	}) bool {
		return r.Pattern == host || r.Pattern == host+"/*"
	}) {
		p.errs.add("%s: no route for %s, the baseURL host in site.yml; add it to routes", file, host)
	}
	needed := slices.Clone(p.endpoints)
	for _, f := range p.workerScripts() {
		b, _ := os.ReadFile(f)
		for _, m := range workerRouteRE.FindAllSubmatch(b, -1) {
			needed = append(needed, string(m[1])+string(m[2]))
		}
	}
	slices.Sort(needed)
	needed = slices.Compact(needed)
	if cfg.Main == "" {
		if len(p.endpoints) > 0 {
			p.errs.add("%s: site.yml lists endpoints (%s) but there is no main Worker to answer them; add main or remove the endpoints", file, strings.Join(p.endpoints, ", "))
		}
		return
	}
	if _, err := os.Stat(filepath.Join(p.Root, cfg.Main)); err != nil {
		p.errs.add("%s: main %s does not exist", file, cfg.Main)
	}
	first := bytes.TrimSpace(cfg.Assets.RunWorkerFirst)
	if string(first) == "true" {
		return
	}
	var patterns []string
	if len(first) > 0 && string(first) != "false" {
		if err := json.Unmarshal(first, &patterns); err != nil {
			p.errs.add("%s: assets.run_worker_first must be true or a list of paths", file)
			return
		}
	}
	var missing []string
	for _, n := range needed {
		if !workerFirst(patterns, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		p.errs.add("%s: assets.run_worker_first does not cover %s; a GET would get the 404 page and a form post 405 instead of reaching the Worker", file, strings.Join(missing, ", "))
	}
}

func workerFirst(patterns []string, pathname string) bool {
	matched := false
	for _, pattern := range patterns {
		pattern, negative := strings.CutPrefix(pattern, "!")
		match, _ := regexp.MatchString("^"+strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*")+"$", pathname)
		if match {
			if negative {
				return false
			}
			matched = true
		}
	}
	return matched
}

func (p *Project) checkSizes() {
	check := func(name string, size int) {
		limit, what := 0, ""
		switch filepath.Ext(name) {
		case ".avif", ".svg":
			limit, what = maxImageBytes, "image"
		case ".css", ".js":
			limit, what = maxCodeBytes, "file"
		}
		if limit > 0 && size > limit {
			p.errs.add("%s: %s is %d KB, over the %d KB budget; shrink it or split it", name, what, size>>10, limit>>10)
		}
	}
	for _, r := range p.resources {
		if r.asset && r.data != nil {
			check("assets/"+r.Name, len(r.data))
		} else if fi, err := os.Stat(filepath.Join(p.Root, r.src)); err == nil {
			check(r.src, int(fi.Size()))
		}
	}
	for _, f := range p.static {
		if fi, err := os.Stat(filepath.Join(p.Root, "static", f)); err == nil {
			check("static/"+f, int(fi.Size()))
		}
	}
}
