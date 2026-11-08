package build

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func anyID(records map[string]*OutRecord, outKeys []string, id string) bool {
	for _, out := range outKeys {
		if slices.Contains(records[out].Scan.IDs, id) {
			return true
		}
	}
	return false
}

func allIDs(records map[string]*OutRecord) []string {
	set := map[string]bool{}
	for _, r := range records {
		for _, id := range r.Scan.IDs {
			set[id] = true
		}
	}
	return keysOf(set)
}

func (p *Project) checkIDRefs(records map[string]*OutRecord, outKeys []string, css *cssInfo, jsWords map[string]bool, static map[string][]byte) {
	var ids []string
	hint := func(id string) string {
		if ids == nil {
			ids = allIDs(records)
		}
		return suggest(id, ids)
	}
	for _, id := range sortedKeys(css.ids) {
		if !anyID(records, outKeys, id) && !jsWords[id] {
			p.errs.add("%s: CSS selector #%s matches no element on any page; delete the rule%s", css.ids[id], id, hint(id))
		}
	}
	refs := map[string]string{}
	for _, out := range outKeys {
		for _, id := range records[out].Scan.ScriptIDs {
			if _, ok := refs[id]; !ok {
				refs[id] = out
			}
		}
	}
	for _, f := range sortedKeys(static) {
		for _, id := range scriptIDs(static[f]) {
			if _, ok := refs[id]; !ok {
				refs[id] = f
			}
		}
	}
	for _, id := range sortedKeys(refs) {
		if anyID(records, outKeys, id) || scriptCreatesID(static, id) {
			continue
		}
		where := refs[id]
		if strings.HasSuffix(where, ".html") {
			p.report(at{out: where}, findNone, id, "script looks up #%s, but no page has an element with that id%s", id, hint(id))
		} else {
			p.errs.add("%s:%d: script looks up #%s, but no page has an element with that id%s", where, lineOf(static[where], []byte(id)), id, hint(id))
		}
	}
}

func scriptCreatesID(static map[string][]byte, id string) bool {
	for _, src := range static {
		for _, pat := range []string{`id="` + id + `"`, `id='` + id + `'`, `.id = "` + id + `"`, `.id="` + id + `"`, `.id = '` + id + `'`, `.id='` + id + `'`} {
			if bytes.Contains(src, []byte(pat)) {
				return true
			}
		}
	}
	return false
}

func lineOf(b, sub []byte) int {
	before, _, ok := bytes.Cut(b, sub)
	if !ok {
		return 1
	}
	return 1 + bytes.Count(before, []byte("\n"))
}

func (p *Project) checkStaticScripts(static map[string][]byte) {
	for _, f := range sortedKeys(static) {
		if !strings.HasPrefix(f, "static/") {
			continue
		}
		if off, msg := jsSyntax(static[f]); msg != "" {
			p.errs.add("%s:%d: %s", f, 1+bytes.Count(static[f][:off], []byte("\n")), msg)
		}
	}
}

func (p *Project) checkImageSizes(records map[string]*OutRecord, outKeys []string, old, ns *State) {
	dims := map[string][2]int{}
	known := map[string][2]int{}
	for _, r := range p.resources {
		if r.w > 0 && r.h > 0 {
			known[r.RelPermalink] = [2]int{r.w, r.h}
		}
	}
	for _, pg := range p.pages {
		if pg.card != nil {
			known[pg.card.RelPermalink] = [2]int{cardW, cardH}
		}
	}
	p.staticSizes.Range(func(k, v any) bool {
		known[k.(string)] = v.([2]int)
		return true
	})
	for _, out := range outKeys {
		for _, entry := range records[out].Scan.ImgSizes {
			f := strings.Fields(entry)
			if len(f) != 3 {
				continue
			}
			src := f[0]
			w, _ := strconv.Atoi(f[1])
			h, _ := strconv.Atoi(f[2])
			target, _, ok := p.internalTarget("/"+strings.TrimSuffix(out, "index.html"), src, at{})
			if !ok || target == "" {
				continue
			}
			d, seen := dims[target]
			if !seen {
				d = p.imageDims(target, old, ns, known)
				dims[target] = d
			}
			if d[0] <= 0 || d[1] <= 0 {
				continue
			}
			fw, fh := float64(d[0]), float64(d[1])
			if math.Abs(float64(w)*fh/fw-float64(h)) > 1 && math.Abs(float64(h)*fw/fh-float64(w)) > 1 {
				p.report(at{out: out}, findLink, src, "<img> declares %d×%d but %s is %d×%d; the image is stretched, so set width and height in the file's proportions (for example %d×%d)", w, h, src, d[0], d[1], w, int(math.Round(float64(w)*fh/fw)))
			}
		}
	}
}

func (p *Project) imageDims(target string, old, ns *State, known map[string][2]int) [2]int {
	dst := strings.TrimPrefix(target, "/")
	stamp := ns.Files[dst]
	if d, ok := known[target]; ok {
		if stamp != "" {
			ns.ImgDims[dst] = stamp + "\x00" + strconv.Itoa(d[0]) + "x" + strconv.Itoa(d[1])
		}
		return d
	}
	if stamp != "" && old != nil {
		if prev, ok := old.ImgDims[dst]; ok {
			if s, size, ok := strings.Cut(prev, "\x00"); ok && s == stamp {
				ws, hs, _ := strings.Cut(size, "x")
				w, _ := strconv.Atoi(ws)
				h, _ := strconv.Atoi(hs)
				ns.ImgDims[dst] = prev
				return [2]int{w, h}
			}
		}
	}
	w, h := p.outputImageSize(target)
	if stamp != "" {
		ns.ImgDims[dst] = stamp + "\x00" + strconv.Itoa(w) + "x" + strconv.Itoa(h)
	}
	return [2]int{w, h}
}

func (p *Project) outputImageSize(target string) (int, int) {
	f, err := os.Open(filepath.Join(p.Out, filepath.FromSlash(strings.TrimPrefix(target, "/"))))
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var data []byte
	for _, n := range []int64{4 << 10, 64 << 10, 1 << 40} {
		more, _ := io.ReadAll(io.LimitReader(f, n-int64(len(data))))
		data = append(data, more...)
		w, h := 0, 0
		if strings.HasSuffix(target, ".svg") {
			if bytes.Contains(data, []byte(">")) {
				w, h = svgSize(data)
			}
		} else {
			w, h = rasterSize(data)
		}
		if w > 0 && h > 0 || len(more) == 0 {
			return w, h
		}
	}
	return 0, 0
}

func (p *Project) checkDates() {
	today := time.Now().Format("2006-01-02")
	for _, pg := range p.pages {
		date, lastmod := "", ""
		if pg.ownDate {
			date = pg.Date.Format("2006-01-02")
		}
		if pg.ownLastmod {
			lastmod = pg.Lastmod.Format("2006-01-02")
		}
		switch {
		case date > today:
			p.errs.add("%s: date %s is in the future; use the publication date", p.fmLoc(pg, "date"), date)
		case lastmod > today:
			p.errs.add("%s: lastmod %s is in the future; use the date of the last real change", p.fmLoc(pg, "lastmod"), lastmod)
		case date != "" && lastmod != "" && pg.Lastmod.Before(pg.Date):
			p.errs.add("%s: lastmod %s is before date %s; a page cannot change before it is published", p.fmLoc(pg, "lastmod"), lastmod, date)
		}
	}
}

func (rs *redirectSet) chains(add func(format string, args ...any)) {
	for _, from := range sortedKeys(rs.dest) {
		to := rs.dest[from]
		line := rs.lines[from]
		switch {
		case to == from:
			add("static/_redirects:%d: %s redirects to itself; remove the rule", line, from)
		case rs.sources[to]:
			seen := map[string]bool{from: true}
			end := to
			for rs.sources[end] && !seen[end] {
				seen[end] = true
				end = rs.dest[end]
			}
			if seen[end] {
				add("static/_redirects:%d: %s is part of a redirect loop through %s; point every rule at a real page", line, from, to)
			} else {
				add("static/_redirects:%d: %s redirects to %s, which redirects again; point it straight at %s", line, from, to, end)
			}
		}
	}
}

func externalLinkProblem(link string) string {
	if link == "" || link[0] == '/' || link[0] == '#' || link[0] == '.' {
		return ""
	}
	if !hasPrefixFold(link, "https://") {
		low := strings.ToLower(link[:min(len(link), 9)])
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "mailto:") || strings.HasPrefix(low, "tel:") {
			return ""
		}
		for _, typo := range []string{"htps:", "htttps:", "hhttps:", "https:", "https//", "https;", "http//", "http:", "ttps://", "htp:"} {
			if strings.HasPrefix(low, typo) {
				return fmt.Sprintf("link %s has a mistyped scheme; write https://", link)
			}
		}
		if strings.HasPrefix(low, "www.") {
			return fmt.Sprintf("link %s has no scheme, so it is treated as a path on this site; write https://%s", link, link)
		}
		return ""
	}
	if strings.ContainsAny(link, " \t\n<>\"\\{}|^`") {
		return fmt.Sprintf("link %q contains characters that must be percent-encoded (spaces, quotes, < >); encode them", link)
	}
	host := link[8:]
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if strings.Contains(host, "@") {
		return fmt.Sprintf("link %s contains credentials before the host; remove them", link)
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		if _, err := strconv.Atoi(host[i+1:]); err == nil {
			host = host[:i]
		}
	}
	if !validHost(host) {
		return fmt.Sprintf("link %s has an invalid host %q; write a domain such as example.no", link, host)
	}
	return ""
}

func validHost(h string) bool {
	if len(h) == 0 || len(h) > 253 || !strings.Contains(h, ".") || strings.HasSuffix(h, ".") {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := range len(l) {
			c := l[i]
			if !(c == '-' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z' || c >= 0x80) {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	for i := range len(tld) {
		if tld[i] >= '0' && tld[i] <= '9' {
			return false
		}
	}
	return len(tld) >= 2
}

var mailtoKeys = []string{"subject", "body", "cc", "bcc", "to"}

func mailtoProblem(href string) string {
	_, query, ok := strings.Cut(href, "?")
	if !ok {
		return ""
	}
	if strings.ContainsAny(query, " \"<>\n") {
		return fmt.Sprintf("email link %s has unencoded spaces or quotes in its subject or body; encode them (space is %%20)", href)
	}
	for part := range strings.SplitSeq(query, "&") {
		k, v, _ := strings.Cut(part, "=")
		if !slices.Contains(mailtoKeys, strings.ToLower(k)) {
			return fmt.Sprintf("email link %s has an unknown field %q; use subject, body, cc or bcc%s", href, k, suggest(k, mailtoKeys))
		}
		if _, err := url.PathUnescape(v); err != nil {
			return fmt.Sprintf("email link %s has an invalid percent escape in %s", href, k)
		}
		if strings.Contains(v, "+") {
			return fmt.Sprintf("email link %s uses + in %s, which mail apps show as a plus sign; write %%20 for spaces", href, k)
		}
	}
	return ""
}
