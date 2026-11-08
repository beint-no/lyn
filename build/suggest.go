package build

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

func suggest(name string, candidates []string) string {
	best, bestDist := "", 1<<30
	lower := strings.ToLower(name)
	for _, c := range candidates {
		d := editDistance(lower, strings.ToLower(c))
		if d < bestDist || (d == bestDist && c < best) {
			best, bestDist = c, d
		}
	}
	if best == "" || bestDist > max(2, len(name)/3) {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func nearest(target string, candidates []string) string {
	seg := lastSegment(target)
	var same []string
	for _, c := range candidates {
		if seg != "" && lastSegment(c) == seg && c != target {
			same = append(same, c)
		}
	}
	if len(same) > 0 {
		slices.SortFunc(same, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b)) })
		return fmt.Sprintf(" (did you mean %s?)", same[0])
	}
	limit := max(2, len(target)/4)
	best, bestDist := "", limit+1
	for _, c := range candidates {
		if d := len(c) - len(target); d > limit || -d > limit {
			continue
		}
		if d := editDistance(target, c); d < bestDist || d == bestDist && c < best {
			best, bestDist = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %s?)", best)
}

func lastSegment(p string) string {
	p = strings.TrimSuffix(p, "/")
	return p[strings.LastIndexByte(p, '/')+1:]
}

func (p *Project) nearestPath(target string, files map[string]bool) string {
	p.pathOnce.Do(func() {
		for f := range files {
			switch {
			case f == "index.html":
				p.pathList = append(p.pathList, "/")
			case strings.HasSuffix(f, "/index.html"):
				p.pathList = append(p.pathList, "/"+strings.TrimSuffix(f, "index.html"))
			case !strings.HasSuffix(f, ".html"):
				p.pathList = append(p.pathList, "/"+f)
			}
		}
		p.pathList = append(p.pathList, p.endpoints...)
	})
	if v, ok := p.pathHints.Load(target); ok {
		return v.(string)
	}
	h := nearest(target, p.pathList)
	p.pathHints.Store(target, h)
	return h
}

func (p *Project) fmLoc(pg *Page, keys ...string) string {
	for _, k := range keys {
		if n := p.frontMatterLine(pg, k); n > 0 {
			return fmt.Sprintf("%s:%d", pg.File, n)
		}
	}
	return pg.File
}
