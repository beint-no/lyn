package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/beint-no/lyn/bench/gen"
	"github.com/beint-no/lyn/build"
)

func site(b *testing.B, n int) string {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("lyn-bench-%d", n))
	if _, err := os.Stat(filepath.Join(dir, "site.yml")); err != nil {
		gen.GenerateLyn(dir, n)
	}
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
