package bench

import (
	"testing"

	"github.com/beint-no/lyn/build"
)

func BenchmarkLoad(b *testing.B) {
	dir := site(b, 5000)
	for b.Loop() {
		if _, err := build.Load(dir); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNoChange(b *testing.B) {
	dir := site(b, 5000)
	bl := build.NewBuilder(dir)
	if _, err := bl.Build(); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		bl := build.NewBuilder(dir)
		if _, err := bl.Build(); err != nil {
			b.Fatal(err)
		}
	}
}
