package build

import "testing"

func TestWorkerFirst(t *testing.T) {
	for _, c := range []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"exact", []string{"/submit"}, "/submit", true},
		{"different path", []string{"/submit"}, "/submit/extra", false},
		{"deep wildcard", []string{"/api/*"}, "/api/private/ping", true},
		{"middle wildcard", []string{"/api/*/ping"}, "/api/private/deep/ping", true},
		{"literal punctuation", []string{"/api/ping.json"}, "/api/pingxjson", false},
		{"excluded path", []string{"/api/*", "!/api/private/*"}, "/api/private/ping", false},
		{"exclusion first", []string{"!/api/private/*", "/api/*"}, "/api/private/ping", false},
		{"outside exclusion", []string{"/api/*", "!/api/private/*"}, "/api/public/ping", true},
		{"only exclusion", []string{"!/api/private/*"}, "/api/public/ping", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := workerFirst(c.patterns, c.path); got != c.want {
				t.Fatalf("workerFirst(%v, %q) = %v; want %v", c.patterns, c.path, got, c.want)
			}
		})
	}
}
