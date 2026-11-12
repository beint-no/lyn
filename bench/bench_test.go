package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/beint-no/lyn/bench/gen"
	"github.com/beint-no/lyn/build"
)

var sites = map[int]string{}

func TestMain(m *testing.M) {
	code := m.Run()
	for _, dir := range sites {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

func site(b *testing.B, n int) string {
	if dir, ok := sites[n]; ok {
		return dir
	}
	dir, err := os.MkdirTemp("", fmt.Sprintf("lyn-bench-%d-", n))
	if err != nil {
		b.Fatal(err)
	}
	gen.GenerateLyn(dir, n)
	sites[n] = dir
	return dir
}

func BenchmarkCold(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			dir := site(b, n)
			for b.Loop() {
				os.RemoveAll(filepath.Join(dir, "public"))
				bl := build.NewBuilder(dir)
				bl.Persist = false
				if _, err := bl.Build(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkIncrementalBody(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			dir := site(b, n)
			bl := build.NewBuilder(dir)
			bl.Persist = false
			if _, err := bl.Build(); err != nil {
				b.Fatal(err)
			}
			p := gen.Plan(n).Pages[n/2]
			file := filepath.Join(dir, "content/nb", p.Section, p.Slug, "index.md")
			i := 0
			for b.Loop() {
				f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
				fmt.Fprintf(f, "\nSetning %d.\n", i)
				f.Close()
				i++
				if _, err := bl.Build(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
