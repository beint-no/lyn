package build

import (
	"fmt"
	"reflect"
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
		for i := range v.NumField() {
			fill(t, v.Field(i), seed)
		}
	default:
		t.Fatalf("state field of kind %s: teach encodeState and decodeState about it", v.Kind())
	}
}

func TestStateRoundTrip(t *testing.T) {
	var s State
	seed := 0
	fill(t, reflect.ValueOf(&s).Elem(), &seed)
	got, err := decodeState(encodeState(&s))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&s, got) {
		t.Fatalf("state does not survive encoding; every State field must be written by encodeState and read by decodeState\nwant %+v\ngot  %+v", s, *got)
	}
	if !sameState(&s, &s) {
		t.Fatal("sameState must hold for identical states")
	}
	raw := encodeState(&s)
	for n := range len(raw) {
		if _, err := decodeState(raw[:n]); err == nil {
			t.Fatalf("truncated state (%d of %d bytes) decoded without error", n, len(raw))
		}
	}
}
