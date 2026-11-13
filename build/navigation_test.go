package build

import (
	"strings"
	"testing"
)

func disconnectedSection(t testing.TB, d string) {
	t.Helper()
	write(t, d, "content/nb/skjult/_index.md", "---\ntitle: Skjulte veiledninger\ndescription: Veiledninger som bare lenker til hverandre og ikke kan finnes fra forsiden.\n---\n")
	write(t, d, "content/nb/skjult/oppgave/index.md", "---\ntitle: En skjult oppgave\ndescription: En oppgave med en lenke tilbake til den skjulte seksjonen i nettstedet.\n---\n[Alle oppgaver](/skjult/)\n")
}

func TestNavigationReachability(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(t *testing.T, d string)
		want string
	}{
		{"reachable section", func(t *testing.T, d string) {
			disconnectedSection(t, d)
			edit(t, d, "layouts/about.html", "<h1>", `<a href="/skjult/">Exercises</a><h1>`)
		}, ""},
		{"translation island", func(t *testing.T, d string) {
			for _, lang := range []string{"nb", "en"} {
				write(t, d, "content/"+lang+"/skjult/index.md", "---\ntitle: Hidden translation page\ndescription: A translated page that is linked only from its own translation on this site.\nlayout: about\n---\n")
			}
			edit(t, d, "layouts/about.html", "<h1>", `{{ range .Translations }}<a href="{{ .RelPermalink }}">Translation</a>{{ end }}<h1>`)
		}, "content/nb/skjult/index.md: /skjult/ is not reachable from a homepage"},
		{"form action", func(t *testing.T, d string) {
			write(t, d, "content/nb/skjult/index.md", "---\ntitle: Hidden form destination\ndescription: A page used only as a form action cannot be reached by following navigation links.\nlayout: about\n---\n")
			edit(t, d, "layouts/about.html", "<h1>", `<form action="/skjult/" method="post"><button>Send</button></form><h1>`)
		}, "content/nb/skjult/index.md: /skjult/ is not reachable from a homepage"},
		{"head link", func(t *testing.T, d string) {
			write(t, d, "content/nb/skjult/index.md", "---\ntitle: Hidden metadata destination\ndescription: A page linked only from metadata cannot be reached through navigation links.\nlayout: about\n---\n")
			edit(t, d, "layouts/base.html", `<link rel="icon"`, `<link rel="help" href="/skjult/"><link rel="icon"`)
		}, "content/nb/skjult/index.md: /skjult/ is not reachable from a homepage"},
		{"noindex orphan", func(t *testing.T, d string) {
			write(t, d, "content/nb/skjult/index.md", "---\ntitle: Hidden advertising page\ndescription: A noindex advertising page does not need an incoming navigation link.\nlayout: about\nnoindex: true\n---\n")
			edit(t, d, "layouts/base.html", "<head>", `<head>{{ if .NoIndex }}<meta name="robots" content="noindex">{{ end }}`)
		}, ""},
		{"noindex intermediary", func(t *testing.T, d string) {
			disconnectedSection(t, d)
			edit(t, d, "content/nb/skjult/_index.md", "title: Skjulte veiledninger", "title: Skjulte veiledninger\nnoindex: true")
			edit(t, d, "layouts/about.html", "<h1>", `<a href="/skjult/">Exercises</a><h1>`)
			edit(t, d, "layouts/base.html", "<head>", `<head>{{ if .NoIndex }}<meta name="robots" content="noindex">{{ end }}`)
		}, "content/nb/skjult/oppgave/index.md: /skjult/oppgave/ is not reachable from a homepage"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := site(t)
			c.mut(t, d)
			err := buildErr(d)
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q; got %v", c.want, err)
			}
		})
	}
}
