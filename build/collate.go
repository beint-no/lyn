package build

import (
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

var collators sync.Map

var sharedCollation = map[string]string{"nb": "nn", "no": "nn"}

func collator(locale string) *sync.Pool {
	if p, ok := collators.Load(locale); ok {
		return p.(*sync.Pool)
	}
	base, _ := language.Make(locale).Base()
	tag := language.Make(base.String())
	if alt, ok := sharedCollation[base.String()]; ok {
		tag = language.Make(alt)
	}
	p := &sync.Pool{New: func() any { return collate.New(tag) }}
	actual, _ := collators.LoadOrStore(locale, p)
	return actual.(*sync.Pool)
}

func collateStrings(locale, a, b string) int {
	pool := collator(locale)
	c := pool.Get().(*collate.Collator)
	defer pool.Put(c)
	return c.CompareString(a, b)
}

func collationKey(locale, title string) []byte {
	pool := collator(locale)
	c := pool.Get().(*collate.Collator)
	defer pool.Put(c)
	var buf collate.Buffer
	return append([]byte(nil), c.KeyFromString(&buf, title)...)
}
