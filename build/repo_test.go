package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) (string, string) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "group", "b")
	copyTree(t, "../example", a)
	copyTree(t, "../example", b)
	edit(t, b, "site.yml", "baseURL: https://example.no/", "baseURL: https://sister.no/")
	write(t, b, "static/_redirects", "/gammel/ /guider/ 301\n")
	return a, b
}

func TestRepoSiteLinks(t *testing.T) {
	a, _ := repo(t)
	good := `<a class="more" href="https://sister.no/guider/mva/">a</a><a class="more" href="https://sister.no/en/guides/">b</a><a class="more" href="https://sister.no/guider/mva/mva.svg">c</a><a class="more" href="https://sister.no/favicon.svg">d</a><a class="more" href="https://sister.no/guider/card.svg">e</a><a class="more" href="https://sister.no/og-image.png">f</a><a class="more" href="https://sister.no/guider/mva/#satser">g</a>`
	edit(t, a, "layouts/about.html", "<h1>", good+"<h1>")
	if err := buildErr(a); err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{
		"https://sister.no/finnes-ikke/":   "broken link https://sister.no/finnes-ikke/: no such page or file on https://sister.no",
		"https://www.sister.no/guider/":    "goes to www; link to https://sister.no/guider/",
		"https://sister.no/gammel/":        "goes through a redirect on https://sister.no; link to https://sister.no/guider/",
		"https://sister.no/guider":         "must end with a slash",
		"https://sister.no/guider/mva.svg": "broken link https://sister.no/guider/mva.svg",
	} {
		t.Run(link, func(t *testing.T) {
			a, _ := repo(t)
			edit(t, a, "layouts/about.html", "<h1>", `<a class="more" href="`+link+`">x</a><h1>`)
			err := buildErr(a)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}
