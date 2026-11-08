package build

import (
	"slices"
	"testing"
)

func TestNorwegianCollation(t *testing.T) {
	got := []string{"Åre", "øl", "Zebra", "æble", "abc"}
	slices.SortFunc(got, func(a, b string) int { return collateStrings("nb-NO", a, b) })
	want := []string{"abc", "Zebra", "æble", "øl", "Åre"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
