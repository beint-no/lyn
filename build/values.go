package build

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/beint-no/lyn/yamlite"
)

func truth(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case HTML:
		return t != ""
	case Attr:
		return t != ""
	case int:
		return t != 0
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case Pages:
		return len(t) > 0
	case resList:
		return len(t) > 0
	case *yamlite.Map:
		return t.Len() > 0
	case *Page:
		return t != nil
	case time.Time:
		return !t.IsZero()
	}
	return true
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "string"
	case HTML:
		return "HTML"
	case int:
		return "int"
	case float64:
		return "float"
	case bool:
		return "bool"
	case []any:
		return "list"
	case Pages:
		return "pages"
	case *yamlite.Map:
		return "map"
	case *Page:
		return "page"
	case *Site:
		return "site"
	case *Resource:
		return "resource"
	case resList:
		return "resources"
	case *MenuEntry:
		return "menu entry"
	case *Language:
		return "language"
	case time.Time:
		return "time"
	case *shortcodeCtx:
		return "shortcode"
	}
	return fmt.Sprintf("%T", v)
}

func iterate(s *st, v any, loc string, each func(k, e any) bool) {
	switch t := v.(type) {
	case nil:
	case []any:
		for i, e := range t {
			if !each(i, e) {
				return
			}
		}
	case Pages:
		for i, e := range t {
			if !each(i, e) {
				return
			}
		}
	case resList:
		for i, e := range t {
			s.r.useResource(e)
			if !each(i, e) {
				return
			}
		}
	case *yamlite.Map:
		for i, k := range t.Keys {
			s.r.read(t, i)
			if !each(k, t.Vals[i]) {
				return
			}
		}
	case int:
		for i := range t {
			if !each(i, i) {
				return
			}
		}
	default:
		fail(loc, "cannot range over %s", typeName(v))
	}
}

func toString(v any, loc string) string {
	switch t := v.(type) {
	case string:
		return t
	case HTML:
		return string(t)
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case Attr:
		return string(t)
	case JSON:
		return string(t)
	}
	fail(loc, "expected a string, got %s", typeName(v))
	return ""
}

func toInt(v any, loc string) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	fail(loc, "expected a number, got %s %v", typeName(v), v)
	return 0
}

func toFloat(v any, loc string) float64 {
	switch t := v.(type) {
	case int:
		return float64(t)
	case float64:
		return t
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return f
		}
	}
	fail(loc, "expected a number, got %s", typeName(v))
	return 0
}

func equal(a, b any) bool {
	switch x := a.(type) {
	case string:
		switch y := b.(type) {
		case string:
			return x == y
		case HTML:
			return x == string(y)
		}
	case HTML:
		switch y := b.(type) {
		case string:
			return string(x) == y
		case HTML:
			return x == y
		}
	case int:
		switch y := b.(type) {
		case int:
			return x == y
		case float64:
			return float64(x) == y
		}
	case float64:
		switch y := b.(type) {
		case int:
			return x == float64(y)
		case float64:
			return x == y
		}
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	case *Page:
		y, ok := b.(*Page)
		return ok && x == y
	case *yamlite.Map:
		y, ok := b.(*yamlite.Map)
		return ok && x == y
	case *MenuEntry:
		y, ok := b.(*MenuEntry)
		return ok && x == y
	case *Language:
		y, ok := b.(*Language)
		return ok && x == y
	case *Resource:
		y, ok := b.(*Resource)
		return ok && x == y
	}
	return false
}

func comparable(a, b any) bool {
	switch a.(type) {
	case nil, string, HTML, int, float64, bool, *Page, *yamlite.Map, *MenuEntry, *Language, *Resource:
	default:
		return false
	}
	switch b.(type) {
	case nil, string, HTML, int, float64, bool, *Page, *yamlite.Map, *MenuEntry, *Language, *Resource:
		return true
	}
	return false
}

func compare(a, b any, loc string) int {
	switch x := a.(type) {
	case int:
		switch y := b.(type) {
		case int:
			return cmp.Compare(x, y)
		case float64:
			return cmp.Compare(float64(x), y)
		}
	case float64:
		return cmp.Compare(x, toFloat(b, loc))
	case string:
		return strings.Compare(x, toString(b, loc))
	case HTML:
		return strings.Compare(string(x), toString(b, loc))
	case time.Time:
		if y, ok := b.(time.Time); ok {
			return x.Compare(y)
		}
	}
	fail(loc, "cannot compare %s with %s", typeName(a), typeName(b))
	return 0
}

func writeValue(s *st, dst []byte, v any, mode outMode, loc string) []byte {
	switch mode {
	case outText, outRaw:
		switch t := v.(type) {
		case HTML:
			if mode == outRaw {
				return escapeText(dst, string(t))
			}
			return append(dst, t...)
		case string:
			return escapeText(dst, t)
		case int:
			return strconv.AppendInt(dst, int64(t), 10)
		case float64:
			return strconv.AppendFloat(dst, t, 'f', -1, 64)
		case Attr:
			fail(loc, "attribute output (attr) used in text")
		}
	case outAttr, outURL, outStyleAttr:
		var str string
		switch t := v.(type) {
		case string:
			str = t
		case HTML:
			str = stripHTML(string(t))
		case int:
			return strconv.AppendInt(dst, int64(t), 10)
		case float64:
			return strconv.AppendFloat(dst, t, 'f', -1, 64)
		case bool:
			return strconv.AppendBool(dst, t)
		case JSON:
			str = string(t)
		default:
			fail(loc, "cannot write %s into an attribute", typeName(v))
		}
		if mode == outURL && !safeURL(str) {
			fail(loc, "unsafe URL %q", str)
		}
		if mode == outStyleAttr && !safeCSSValue(str) {
			fail(loc, "unsafe CSS value %q", str)
		}
		return escapeHTML(dst, str)
	case outTagAttrs:
		if t, ok := v.(Attr); ok {
			if t == "" {
				return dst
			}
			if len(dst) > 0 && dst[len(dst)-1] != ' ' {
				dst = append(dst, ' ')
			}
			return append(dst, t...)
		}
		fail(loc, "only attr values can be written inside a tag, got %s", typeName(v))
	case outScript:
		switch t := v.(type) {
		case JSON:
			return append(dst, t...)
		case int:
			return strconv.AppendInt(dst, int64(t), 10)
		case bool:
			return strconv.AppendBool(dst, t)
		}
		fail(loc, "inside <script> only json output is allowed; pipe the value to json")
	case outStyle:
		str := toString(v, loc)
		if !safeCSSValue(str) {
			fail(loc, "unsafe CSS value %q", str)
		}
		return append(dst, str...)
	}
	if v == nil {
		fail(loc, "nothing to print (the value is missing)")
	}
	fail(loc, "cannot print %s", typeName(v))
	return dst
}

func appendJSON(dst []byte, v any, loc string) []byte {
	switch t := v.(type) {
	case nil:
		return append(dst, "null"...)
	case string:
		return appendJSONString(dst, t)
	case HTML:
		return appendJSONString(dst, string(t))
	case int:
		return strconv.AppendInt(dst, int64(t), 10)
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			fail(loc, "json: invalid number")
		}
		return strconv.AppendFloat(dst, t, 'f', -1, 64)
	case bool:
		return strconv.AppendBool(dst, t)
	case JSON:
		return append(dst, t...)
	case []any:
		dst = append(dst, '[')
		for i, e := range t {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendJSON(dst, e, loc)
		}
		return append(dst, ']')
	case *yamlite.Map:
		dst = append(dst, '{')
		for i, k := range t.Keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			t.MarkRead(i)
			dst = appendJSONString(dst, k)
			dst = append(dst, ':')
			dst = appendJSON(dst, t.Vals[i], loc)
		}
		return append(dst, '}')
	case time.Time:
		return appendJSONString(dst, t.Format(time.RFC3339))
	}
	fail(loc, "json: cannot encode %s", typeName(v))
	return dst
}

func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			dst = append(dst, '\\', byte(r))
		case r == '\n':
			dst = append(dst, '\\', 'n')
		case r == '\r':
			dst = append(dst, '\\', 'r')
		case r == '\t':
			dst = append(dst, '\\', 't')
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029:
			dst = append(dst, '\\', 'u', hex[r>>12&15], hex[r>>8&15], hex[r>>4&15], hex[r&15])
		default:
			dst = append(dst, string(r)...)
		}
	}
	return append(dst, '"')
}
