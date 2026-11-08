package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/beint-no/lyn/bench/gen"
	"github.com/beint-no/lyn/build"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func run(dir string, name string, args ...string) time.Duration {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	if err := cmd.Run(); err != nil {
		panic(fmt.Sprintf("%s %v in %s: %v\n%s", name, args, dir, err, out.String()))
	}
	return time.Since(start)
}

func median(ds []time.Duration) time.Duration {
	slices.Sort(ds)
	return ds[len(ds)/2]
}

func appendTo(path, text string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	must(err)
	f.WriteString(text)
	f.Close()
}

func replaceIn(path, old, new string) {
	b, err := os.ReadFile(path)
	must(err)
	must(os.WriteFile(path, bytes.Replace(b, []byte(old), []byte(new), 1), 0o644))
}

type edit struct {
	name string
	lyn  func(dir string, i int)
	hugo func(dir string, i int)
	file string
}

func edits(n int) []edit {
	p := gen.Plan(n).Pages[n/2]
	page := p.Section + "/" + p.Slug + "/index.md"
	return []edit{
		{name: "body edit", file: "content/nb/" + page, lyn: func(d string, i int) {
			appendTo(filepath.Join(d, "content/nb", page), fmt.Sprintf("\nNy setning %d.\n", i))
		},
			hugo: func(d string, i int) {
				appendTo(filepath.Join(d, "content", page), fmt.Sprintf("\nNy setning %d.\n", i))
			}},
		{name: "title edit", file: "content/nb/" + page, lyn: func(d string, i int) {
			replaceIn(filepath.Join(d, "content/nb", page), "title: ", fmt.Sprintf("title: Endret%d ", i))
		}, hugo: func(d string, i int) {
			replaceIn(filepath.Join(d, "content", page), "title: ", fmt.Sprintf("title: Endret%d ", i))
		}},
		{name: "partial edit", file: "layouts/partials/card.html", lyn: func(d string, i int) {
			replaceIn(filepath.Join(d, "layouts/partials/card.html"), "<p>{{ .Summary }}", fmt.Sprintf("<p>%d {{ .Summary }}", i))
		}, hugo: func(d string, i int) {
			replaceIn(filepath.Join(d, "layouts/_partials/card.html"), "<p>{{ .Summary }}", fmt.Sprintf("<p>%d {{ .Summary }}", i))
		}},
	}
}

var totalRE = regexp.MustCompile(`(?:Total|Built) in (\d+) ms`)

type hugoServer struct {
	cmd   *exec.Cmd
	lines chan string
}

func startHugo(dir string) *hugoServer {
	cmd := exec.Command("hugo", "server", "--port", "1399", "--disableLiveReload", "--disableFastRender", "--renderToMemory=false")
	cmd.Dir = dir
	pr, pw, _ := os.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	must(cmd.Start())
	h := &hugoServer{cmd: cmd, lines: make(chan string, 1000)}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
	}()
	h.wait(5 * time.Minute)
	return h
}

func (h *hugoServer) wait(timeout time.Duration) time.Duration {
	deadline := time.After(timeout)
	for {
		select {
		case l := <-h.lines:
			if m := totalRE.FindStringSubmatch(l); m != nil {
				ms, _ := strconv.Atoi(m[1])
				return time.Duration(ms) * time.Millisecond
			}
			if strings.Contains(l, "ERROR") {
				panic(l)
			}
		case <-deadline:
			panic("hugo server timed out")
		}
	}
}

func (h *hugoServer) stop() {
	h.cmd.Process.Kill()
	h.cmd.Wait()
}

func main() {
	sizes := flag.String("sizes", "100,1000,5000", "page counts")
	runs := flag.Int("runs", 3, "runs per measurement")
	root := flag.String("dir", os.TempDir(), "work directory")
	lynBin := flag.String("lyn", "lyn", "lyn binary")
	flag.Parse()
	fmt.Println("| pages | measurement | hugo + safety hooks | hugo plain | lyn (CLI) | lyn (serve, in-process) | speedup vs hugo + hooks |")
	fmt.Println("|---:|---|---:|---:|---:|---:|---:|")
	for s := range strings.SplitSeq(*sizes, ",") {
		n, _ := strconv.Atoi(s)
		base := filepath.Join(*root, "bench-"+s)
		os.RemoveAll(base)
		lyn, hugo, plain := filepath.Join(base, "lyn"), filepath.Join(base, "hugo"), filepath.Join(base, "hugo-plain")
		gen.GenerateLyn(lyn, n)
		gen.GenerateHugo(hugo, n, true)
		gen.GenerateHugo(plain, n, false)

		var hc, pc, lc []time.Duration
		for range *runs {
			for _, d := range []string{hugo, plain} {
				os.RemoveAll(filepath.Join(d, "public"))
				os.RemoveAll(filepath.Join(d, "resources"))
			}
			os.RemoveAll(filepath.Join(lyn, "public"))
			os.RemoveAll(filepath.Join(lyn, ".lyn"))
			hc = append(hc, run(hugo, "hugo", "--quiet"))
			pc = append(pc, run(plain, "hugo", "--quiet"))
			lc = append(lc, run(lyn, *lynBin, "build"))
		}
		row(n, "cold build", median(hc), median(pc), median(lc), 0)

		var hn, ln []time.Duration
		for range *runs {
			hn = append(hn, run(hugo, "hugo", "--quiet"))
			ln = append(ln, run(lyn, *lynBin, "build"))
		}
		row(n, "rebuild, nothing changed", median(hn), 0, median(ln), 0)

		b := build.NewBuilder(lyn)
		b.Persist = false
		if _, err := b.Build(); err != nil {
			panic(err)
		}
		hs := startHugo(hugo)
		for _, e := range edits(n) {
			var he, le, li []time.Duration
			for i := range *runs {
				e.hugo(hugo, i)
				he = append(he, hs.wait(2*time.Minute))
				e.lyn(lyn, 100+i)
				le = append(le, run(lyn, *lynBin, "build"))
				e.lyn(lyn, 200+i)
				b.Changed(filepath.Join(lyn, e.file), false)
				start := time.Now()
				if _, err := b.Build(); err != nil {
					panic(err)
				}
				li = append(li, time.Since(start))
			}
			row(n, e.name, median(he), 0, median(le), median(li))
		}
		hs.stop()
	}
}

func ms(d time.Duration) string {
	if d == 0 {
		return "–"
	}
	if d < 10*time.Millisecond {
		return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

func row(n int, what string, hugo, plain, lynCLI, lynMem time.Duration) {
	best := lynCLI
	if lynMem > 0 && lynMem < best {
		best = lynMem
	}
	fmt.Printf("| %d | %s | %s | %s | %s | %s | %.1f× |\n", n, what, ms(hugo), ms(plain), ms(lynCLI), ms(lynMem), float64(hugo)/float64(best))
}
