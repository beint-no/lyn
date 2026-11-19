package main

import "testing"

func TestLynVersion(t *testing.T) {
	if v := lynVersion(); v == "" {
		t.Fatal("empty version")
	}
}
