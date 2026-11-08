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
	p := &sync.Pool{New: func() any { return &keyer{c: collate.New(tag)} }}
	actual, _ := collators.LoadOrStore(locale, p)
	return actual.(*sync.Pool)
}

type keyer struct {
	c   *collate.Collator
	buf collate.Buffer
}

func collateStrings(locale, a, b string) int {
	pool := collator(locale)
	k := pool.Get().(*keyer)
	defer pool.Put(k)
	return k.c.CompareString(a, b)
}

func collationKey(locale, title string) []byte {
	pool := collator(locale)
	k := pool.Get().(*keyer)
	defer pool.Put(k)
	key := append([]byte(nil), k.c.KeyFromString(&k.buf, title)...)
	k.buf.Reset()
	return key
}
