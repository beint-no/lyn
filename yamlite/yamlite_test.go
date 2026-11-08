package yamlite

import (
	"reflect"
	"strings"
	"testing"
)

func plain(v any) any {
	switch t := v.(type) {
	case *Map:
		out := map[string]any{}
		for i, k := range t.Keys {
			out[k] = plain(t.Vals[i])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, c := range t {
			out[i] = plain(c)
		}
		return out
	}
	return v
}

func TestParse(t *testing.T) {
	src := `title: Klar for prøven
country: NO
postal: "0150"
postalPlain: 0150
count: 12
ratio: 1.5
negative: -3
on: true
off: false
phone: +47 40734212
orgnr: "935 949 505"
tags: [a, "b c", 3]
empty: []
none: {}
menu:
  - name: Bil
    pageRef: /teori/bil
    weight: 1
  - name: Moped
    params:
      footer: x
list:
- one
- two
quoted: "say \"hi\"\u00e5"
heading: |
  ## Hvor bør du begynne?

  text
literal: |
  line one
    indented

  line three
folded: >-
  one
  two

  three
nested:
  deep:
    deeper: yes
`
	m, err := Parse("t.yml", []byte(src), 1)
	if err != nil {
		t.Fatal(err)
	}
	got := plain(m).(map[string]any)
	want := map[string]any{
		"title":       "Klar for prøven",
		"country":     "NO",
		"postal":      "0150",
		"postalPlain": "0150",
		"count":       12,
		"ratio":       1.5,
		"negative":    -3,
		"on":          true,
		"off":         false,
		"phone":       "+47 40734212",
		"orgnr":       "935 949 505",
		"tags":        []any{"a", "b c", 3},
		"empty":       []any{},
		"none":        map[string]any{},
		"menu": []any{
			map[string]any{"name": "Bil", "pageRef": "/teori/bil", "weight": 1},
			map[string]any{"name": "Moped", "params": map[string]any{"footer": "x"}},
		},
		"list":    []any{"one", "two"},
		"quoted":  "say \"hi\"å",
		"literal": "line one\n  indented\n\nline three\n",
		"heading": "## Hvor bør du begynne?\n\ntext\n",
		"folded":  "one two\nthree",
		"nested":  map[string]any{"deep": map[string]any{"deeper": "yes"}},
	}
	for k, w := range want {
		if !reflect.DeepEqual(got[k], w) {
			t.Errorf("%s: got %#v want %#v", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys want %d", len(got), len(want))
	}
}

func TestRejects(t *testing.T) {
	cases := map[string]string{
		"a: 1\na: 2\n":          "duplicate key",
		"# c\na: 1\n":           "comments are not allowed",
		"a: x # c\n":            "comments are not allowed",
		"a: 'x'\n":              "single-quoted",
		"a: null\n":             "null is not allowed",
		"a:\nb: 1\n":            "has no value",
		"a: &x 1\n":             "anchors",
		"a: !!str 1\n":          "tags",
		"a: {b: 1}\n":           "flow mappings",
		"a: b: c\n":             "quote it",
		"a:\n\tb: 1\n":          "tabs",
		"a: 1\n---\nb: 2\n":     "multiple documents",
		"a:\n  b: 1\n   c: 2\n": "unexpected indentation",
		"- a\n":                 "root must be a mapping",
		"a: [1, [2]]\n":         "only contain scalars",
		"a: \"open\n":           "unterminated",
	}
	for src, want := range cases {
		_, err := Parse("t.yml", []byte(src), 1)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", src, err, want)
		}
	}
}

func TestErrorLine(t *testing.T) {
	_, err := Parse("f.md", []byte("title: x\ndescription: a: b\n"), 2)
	if err == nil || !strings.HasPrefix(err.Error(), "f.md:3:") {
		t.Fatalf("got %v", err)
	}
}

func TestFrontMatter(t *testing.T) {
	fm, body, ok := SplitFrontMatter([]byte("---\ntitle: x\n---\n# Hi\n"))
	if !ok || string(fm) != "title: x\n" || string(body) != "# Hi\n" {
		t.Fatalf("%q %q %v", fm, body, ok)
	}
}

func TestReadTracking(t *testing.T) {
	m, _ := Parse("t", []byte("a: 1\nb: 2\n"), 1)
	m.Track()
	m.Get("a")
	if !m.WasRead(0) || m.WasRead(1) {
		t.Fatal("tracking")
	}
}

func BenchmarkParse(b *testing.B) {
	src := []byte(strings.Repeat("title: Klar for prøven i klasse B\ndescription: En lang beskrivelse av siden som brukes i meta description\nweight: 3\n", 1) + "params:\n  items:\n    - a\n    - b\n")
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		if _, err := Parse("t", src, 1); err != nil {
			b.Fatal(err)
		}
	}
}
