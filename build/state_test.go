package build

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
)

func fill(t *testing.T, v reflect.Value, seed *int) {
	*seed++
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("s%d", *seed%7))
	case reflect.Int:
		v.SetInt(int64(*seed) - 50)
	case reflect.Uint64:
		v.SetUint(uint64(*seed) << 40)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			fill(t, s.Index(i), seed)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		for range 2 {
			k := reflect.New(v.Type().Key()).Elem()
			fill(t, k, seed)
			e := reflect.New(v.Type().Elem()).Elem()
			fill(t, e, seed)
			m.SetMapIndex(k, e)
		}
		v.Set(m)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fill(t, p.Elem(), seed)
		v.Set(p)
	case reflect.Struct:
		for _, field := range v.Fields() {
			fill(t, field, seed)
		}
	default:
		t.Fatalf("state field of kind %s: teach encodeState and decodeState about it", v.Kind())
	}
}

func TestStateRoundTrip(t *testing.T) {
	var s State
	seed := 0
	fill(t, reflect.ValueOf(&s).Elem(), &seed)
	got, base, err := decodeState(encodeState(&s))
	if err != nil || base != len(encodeState(&s)) {
		t.Fatal(err, base)
	}
	if !reflect.DeepEqual(&s, got) {
		t.Fatalf("state does not survive encoding; every State field must be written by encodeState and read by decodeState\nwant %+v\ngot  %+v", s, *got)
	}
	if !sameState(&s, &s) {
		t.Fatal("sameState must hold for identical states")
	}
	raw := encodeState(&s)
	for n := range len(raw) {
		if _, _, err := decodeState(raw[:n]); err == nil {
			t.Fatalf("truncated state (%d of %d bytes) decoded without error", n, len(raw))
		}
	}

	next := *got
	next.Pages = maps.Clone(got.Pages)
	next.Files = maps.Clone(got.Files)
	var pageKeys, fileKeys []string
	for k := range got.Pages {
		pageKeys = append(pageKeys, k)
	}
	for k := range got.Files {
		fileKeys = append(fileKeys, k)
	}
	page := next.Pages[pageKeys[0]]
	page.Body++
	next.Pages[pageKeys[0]] = page
	delete(next.Pages, pageKeys[1])
	next.Pages["added"] = page
	next.Files[fileKeys[0]] = "changed"
	delete(next.Files, fileKeys[1])
	next.Files["added"] = "new file"
	next.Outputs = map[string]*OutRecord{}
	var keys []string
	for k, r := range got.Outputs {
		next.Outputs[k] = r
		keys = append(keys, k)
	}
	changed := *next.Outputs[keys[0]]
	changed.Hash++
	next.Outputs[keys[0]] = &changed
	delete(next.Outputs, keys[1])
	added := changed
	added.Page = "added"
	next.Outputs["added"] = &added
	next.Year++
	delta := encodeStateSegment(&next, got)
	after, _, err := decodeState(append(slices.Clone(raw), delta...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&next, after) {
		t.Fatalf("state does not survive a delta segment\nwant %+v\ngot  %+v", next, *after)
	}
}
