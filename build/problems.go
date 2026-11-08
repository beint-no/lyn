package build

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type problem struct {
	loc   string
	msg   string
	pages []*Page
}

type errorList struct {
	mu     sync.Mutex
	root   string
	probs  map[string]*problem
	issues []pageIssue
}

type pageIssue struct {
	out   string
	off   int
	kind  byte
	match string
	words []string
	msg   string
}

const (
	findNone byte = iota
	findLink
	findClass
	findAttr
	findTag
)

func (e *errorList) add(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for line := range strings.SplitSeq(msg, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			loc, rest := splitLoc(line)
			e.at(loc, rest, nil)
		}
	}
}

func (e *errorList) at(loc, msg string, pg *Page) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.probs == nil {
		e.probs = map[string]*problem{}
	}
	key := loc + "\x00" + msg
	pr := e.probs[key]
	if pr == nil {
		pr = &problem{loc: loc, msg: msg}
		e.probs[key] = pr
	}
	if pg != nil && !slices.Contains(pr.pages, pg) {
		pr.pages = append(pr.pages, pg)
	}
}

func (e *errorList) issue(is pageIssue) {
	e.mu.Lock()
	e.issues = append(e.issues, is)
	e.mu.Unlock()
}

func splitLoc(s string) (string, string) {
	i := strings.Index(s, ": ")
	if i <= 0 {
		return "", s
	}
	loc := s[:i]
	if strings.ContainsAny(loc, " \"'()") || !strings.ContainsAny(loc, "./") && loc != "i18n" {
		return "", s
	}
	return loc, s[i+2:]
}

func locParts(loc string) (string, int, int) {
	file, line, col := loc, 0, 0
	if i := strings.LastIndexByte(file, ':'); i > 0 {
		if n, err := strconv.Atoi(file[i+1:]); err == nil {
			file, line = file[:i], n
			if j := strings.LastIndexByte(file, ':'); j > 0 {
				if m, err := strconv.Atoi(file[j+1:]); err == nil {
					file, line, col = file[:j], m, n
				}
			}
		}
	}
	return file, line, col
}

func (e *errorList) err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.probs) == 0 {
		return nil
	}
	list := make([]*problem, 0, len(e.probs))
	for _, pr := range e.probs {
		list = append(list, pr)
	}
	slices.SortFunc(list, func(a, b *problem) int {
		fa, la, ca := locParts(a.loc)
		fb, lb, cb := locParts(b.loc)
		return cmp.Or(cmp.Compare(fa, fb), cmp.Compare(la, lb), cmp.Compare(ca, cb), cmp.Compare(a.msg, b.msg))
	})
	sources := map[string][][]byte{}
	var b strings.Builder
	fmt.Fprintf(&b, "%d problem(s):", len(list))
	for _, pr := range list {
		b.WriteString("\n  ")
		if pr.loc != "" {
			b.WriteString(pr.loc)
			b.WriteString(": ")
		}
		b.WriteString(pr.msg)
		file, line, _ := locParts(pr.loc)
		if ex := e.excerpt(sources, file, line); ex != "" {
			b.WriteString("\n      | ")
			b.WriteString(ex)
		}
		b.WriteString(pageList(pr.pages, file))
	}
	return fmt.Errorf("%s", b.String())
}

func pageList(pages []*Page, file string) string {
	var urls []string
	for _, pg := range pages {
		if pg.File != file || file == "" {
			urls = append(urls, pg.RelPermalink)
		}
	}
	if len(urls) == 0 {
		return ""
	}
	slices.Sort(urls)
	if len(urls) == 1 {
		return "\n      on " + urls[0]
	}
	shown := urls[:min(3, len(urls))]
	more := ""
	if len(urls) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(urls)-len(shown))
	}
	return fmt.Sprintf("\n      on %d pages: %s%s", len(urls), strings.Join(shown, ", "), more)
}

func (e *errorList) excerpt(cache map[string][][]byte, file string, line int) string {
	if line <= 0 || e.root == "" {
		return ""
	}
	lines, ok := cache[file]
	if !ok {
		b, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(file)))
		if err == nil && bytes.IndexByte(b[:min(len(b), 8000)], 0) < 0 {
			lines = bytes.Split(b, []byte("\n"))
		}
		cache[file] = lines
	}
	if line > len(lines) || len(lines[line-1]) > 400 {
		return ""
	}
	s := strings.Join(strings.Fields(string(lines[line-1])), " ")
	if len(s) > 160 {
		cut := 157
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}
