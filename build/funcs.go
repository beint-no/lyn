package build

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/beint-no/lyn/yamlite"
)

type builtin struct {
	min, max int
	fn       func(s *st, a []any, loc string) any
}

var builtins map[string]builtin

var regexCache sync.Map

func regex(pattern, loc string) *regexp.Regexp {
	if r, ok := regexCache.Load(pattern); ok {
		return r.(*regexp.Regexp)
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		fail(loc, "invalid regular expression %q: %v", pattern, err)
	}
	regexCache.Store(pattern, r)
	return r
}

func asList(v any, loc string) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	case Pages:
		out := make([]any, len(t))
		for i, p := range t {
			out[i] = p
		}
		return out
	case resList:
		out := make([]any, len(t))
		for i, p := range t {
			out[i] = p
		}
		return out
	}
	fail(loc, "expected a list, got %s", typeName(v))
	return nil
}

func sameKind(orig any, items []any) any {
	if _, ok := orig.(Pages); ok {
		out := make(Pages, len(items))
		for i, x := range items {
			out[i] = x.(*Page)
		}
		return out
	}
	if _, ok := orig.(resList); ok {
		out := make(resList, len(items))
		for i, x := range items {
			out[i] = x.(*Resource)
		}
		return out
	}
	return items
}

func length(v any, loc string) int {
	switch t := v.(type) {
	case nil:
		return 0
	case string:
		return utf8.RuneCountInString(t)
	case HTML:
		return utf8.RuneCountInString(string(t))
	case []any:
		return len(t)
	case Pages:
		return len(t)
	case resList:
		return len(t)
	case *yamlite.Map:
		return t.Len()
	}
	fail(loc, "len: cannot measure %s", typeName(v))
	return 0
}

func resolvePath(s *st, v any, key, loc string) any {
	for _, part := range strings.Split(strings.TrimPrefix(key, "."), ".") {
		a := lookupAccessor(part)
		if a == nil {
			fail(loc, "unknown field %q in %q", part, key)
		}
		if v == nil {
			return nil
		}
		if m, ok := v.(*yamlite.Map); ok && a.key != "" {
			i := m.Find(part)
			if i < 0 {
				return nil
			}
			s.r.read(m, i)
			v = m.Vals[i]
			continue
		}
		v = a.get(s, v, nil, loc)
	}
	return v
}

func matchOp(op string, a, b any, loc string) bool {
	switch op {
	case "=", "==", "eq":
		return equal(a, b)
	case "!=", "<>", "ne":
		return !equal(a, b)
	case ">", "gt":
		return a != nil && compare(a, b, loc) > 0
	case ">=", "ge":
		return a != nil && compare(a, b, loc) >= 0
	case "<", "lt":
		return a != nil && compare(a, b, loc) < 0
	case "<=", "le":
		return a != nil && compare(a, b, loc) <= 0
	case "in":
		return contains(b, a, loc)
	case "not in":
		return !contains(b, a, loc)
	}
	fail(loc, "where: unknown operator %q", op)
	return false
}

func contains(coll, item any, loc string) bool {
	switch t := coll.(type) {
	case string:
		return strings.Contains(t, toString(item, loc))
	case HTML:
		return strings.Contains(string(t), toString(item, loc))
	case nil:
		return false
	}
	for _, e := range asList(coll, loc) {
		if equal(e, item) {
			return true
		}
	}
	return false
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			dash = false
		case r == '-' || unicode.IsSpace(r):
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func plainify(s string) string {
	s = tagRE.ReplaceAllString(s, "")
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", "\"", "&#39;", "'", "&quot;", "\"", "&nbsp;", " ")
	return r.Replace(s)
}

func init() {
	cmp2 := func(f func(c int) bool) func(s *st, a []any, loc string) any {
		return func(s *st, a []any, loc string) any { return f(compare(a[0], a[1], loc)) }
	}
	num := func(f func(x, y float64) float64, fi func(x, y int) int) func(s *st, a []any, loc string) any {
		return func(s *st, a []any, loc string) any {
			x, xi := a[0].(int)
			y, yi := a[1].(int)
			if xi && yi && fi != nil {
				return fi(x, y)
			}
			return f(toFloat(a[0], loc), toFloat(a[1], loc))
		}
	}
	builtins = map[string]builtin{
		"not": {1, 1, func(s *st, a []any, _ string) any { return !truth(a[0]) }},
		"eq": {2, -1, func(s *st, a []any, loc string) any {
			for _, b := range a[1:] {
				if !comparable(a[0], b) {
					fail(loc, "eq: cannot compare %s with %s", typeName(a[0]), typeName(b))
				}
				if equal(a[0], b) {
					return true
				}
			}
			return false
		}},
		"ne": {2, 2, func(s *st, a []any, loc string) any {
			if !comparable(a[0], a[1]) {
				fail(loc, "ne: cannot compare %s with %s", typeName(a[0]), typeName(a[1]))
			}
			return !equal(a[0], a[1])
		}},
		"lt":  {2, 2, cmp2(func(c int) bool { return c < 0 })},
		"le":  {2, 2, cmp2(func(c int) bool { return c <= 0 })},
		"gt":  {2, 2, cmp2(func(c int) bool { return c > 0 })},
		"ge":  {2, 2, cmp2(func(c int) bool { return c >= 0 })},
		"len": {1, 1, func(s *st, a []any, loc string) any { return length(a[0], loc) }},
		"index": {2, -1, func(s *st, a []any, loc string) any {
			v := a[0]
			for _, k := range a[1:] {
				switch t := v.(type) {
				case *yamlite.Map:
					i := t.Find(toString(k, loc))
					if i < 0 {
						return nil
					}
					s.r.read(t, i)
					v = t.Vals[i]
				case nil:
					return nil
				default:
					list := asList(v, loc)
					i := toInt(k, loc)
					if i < 0 || i >= len(list) {
						fail(loc, "index %d out of range (length %d)", i, len(list))
					}
					v = list[i]
				}
			}
			return v
		}},
		"first": {2, 2, func(s *st, a []any, loc string) any {
			n := toInt(a[0], loc)
			l := asList(a[1], loc)
			return sameKind(a[1], l[:min(n, len(l))])
		}},
		"last": {2, 2, func(s *st, a []any, loc string) any {
			n := toInt(a[0], loc)
			l := asList(a[1], loc)
			return sameKind(a[1], l[max(0, len(l)-n):])
		}},
		"after": {2, 2, func(s *st, a []any, loc string) any {
			n := toInt(a[0], loc)
			l := asList(a[1], loc)
			return sameKind(a[1], l[min(n, len(l)):])
		}},
		"in": {2, 2, func(s *st, a []any, loc string) any { return contains(a[0], a[1], loc) }},
		"where": {3, 4, func(s *st, a []any, loc string) any {
			key := toString(a[1], loc)
			op, want := "=", a[2]
			if len(a) == 4 {
				op, want = toString(a[2], loc), a[3]
			}
			var out []any
			for _, e := range asList(a[0], loc) {
				if matchOp(op, resolvePath(s, e, key, loc), want, loc) {
					out = append(out, e)
				}
			}
			return sameKind(a[0], out)
		}},
		"sort": {1, 3, func(s *st, a []any, loc string) any {
			l := slices.Clone(asList(a[0], loc))
			key, desc := "", false
			if len(a) > 1 {
				key = toString(a[1], loc)
			}
			if len(a) > 2 {
				switch toString(a[2], loc) {
				case "desc":
					desc = true
				case "asc":
				default:
					fail(loc, "sort order must be asc or desc")
				}
			}
			keys := make([]any, len(l))
			for i, e := range l {
				if key == "" {
					keys[i] = e
				} else {
					keys[i] = resolvePath(s, e, key, loc)
				}
			}
			idx := make([]int, len(l))
			for i := range idx {
				idx[i] = i
			}
			slices.SortStableFunc(idx, func(x, y int) int {
				kx, ky := keys[x], keys[y]
				var c int
				switch {
				case kx == nil && ky == nil:
					c = 0
				case kx == nil:
					c = 1
				case ky == nil:
					c = -1
				default:
					xs, xok := kx.(string)
					ys, yok := ky.(string)
					if xok && yok {
						c = collateStrings(s.r.site.Language.Locale, xs, ys)
					} else {
						c = compare(kx, ky, loc)
					}
				}
				if desc {
					return -c
				}
				return c
			})
			out := make([]any, len(l))
			for i, j := range idx {
				out[i] = l[j]
			}
			return sameKind(a[0], out)
		}},
		"uniq": {1, 1, func(s *st, a []any, loc string) any {
			var out []any
			for _, e := range asList(a[0], loc) {
				if !slices.ContainsFunc(out, func(x any) bool { return equal(x, e) }) {
					out = append(out, e)
				}
			}
			return sameKind(a[0], out)
		}},
		"reverse": {1, 1, func(s *st, a []any, loc string) any {
			l := slices.Clone(asList(a[0], loc))
			slices.Reverse(l)
			return sameKind(a[0], l)
		}},
		"seq": {1, 2, func(s *st, a []any, loc string) any {
			from, to := 1, toInt(a[0], loc)
			if len(a) == 2 {
				from, to = toInt(a[0], loc), toInt(a[1], loc)
			}
			var out []any
			for i := from; i <= to; i++ {
				out = append(out, i)
			}
			return out
		}},
		"dict": {0, -1, func(s *st, a []any, loc string) any {
			if len(a)%2 != 0 {
				fail(loc, "dict needs key/value pairs")
			}
			m := yamlite.NewMap()
			m.ID = -2
			for i := 0; i < len(a); i += 2 {
				if !m.Set(toString(a[i], loc), a[i+1], 0) {
					fail(loc, "dict: duplicate key %q", a[i])
				}
			}
			return m
		}},
		"list": {0, -1, func(s *st, a []any, loc string) any { return slices.Clone(a) }},
		"append": {2, -1, func(s *st, a []any, loc string) any {
			base := asList(a[len(a)-1], loc)
			out := make([]any, 0, len(base)+len(a)-1)
			out = append(out, base...)
			out = append(out, a[:len(a)-1]...)
			return out
		}},
		"merge": {2, -1, func(s *st, a []any, loc string) any {
			m := yamlite.NewMap()
			m.ID = -2
			for _, x := range a {
				src, ok := x.(*yamlite.Map)
				if !ok {
					fail(loc, "merge needs maps, got %s", typeName(x))
				}
				for i, k := range src.Keys {
					s.r.read(src, i)
					if j := m.Find(k); j >= 0 {
						m.Vals[j] = src.Vals[i]
					} else {
						m.Set(k, src.Vals[i], 0)
					}
				}
			}
			return m
		}},
		"print": {0, -1, func(s *st, a []any, loc string) any {
			var b strings.Builder
			for _, x := range a {
				b.WriteString(toString(x, loc))
			}
			return b.String()
		}},
		"printf": {1, -1, func(s *st, a []any, loc string) any {
			args := make([]any, len(a)-1)
			for i, x := range a[1:] {
				switch t := x.(type) {
				case string, int, float64, bool:
					args[i] = t
				case HTML:
					args[i] = string(t)
				default:
					fail(loc, "printf: cannot format %s", typeName(x))
				}
			}
			out := fmt.Sprintf(toString(a[0], loc), args...)
			if strings.Contains(out, "%!") {
				fail(loc, "printf: bad format %q", a[0])
			}
			return out
		}},
		"lower":     {1, 1, func(s *st, a []any, loc string) any { return strings.ToLower(toString(a[0], loc)) }},
		"upper":     {1, 1, func(s *st, a []any, loc string) any { return strings.ToUpper(toString(a[0], loc)) }},
		"trimSpace": {1, 1, func(s *st, a []any, loc string) any { return strings.TrimSpace(toString(a[0], loc)) }},
		"trim":      {2, 2, func(s *st, a []any, loc string) any { return strings.Trim(toString(a[0], loc), toString(a[1], loc)) }},
		"trimPrefix": {2, 2, func(s *st, a []any, loc string) any {
			return strings.TrimPrefix(toString(a[1], loc), toString(a[0], loc))
		}},
		"trimSuffix": {2, 2, func(s *st, a []any, loc string) any {
			return strings.TrimSuffix(toString(a[1], loc), toString(a[0], loc))
		}},
		"hasPrefix": {2, 2, func(s *st, a []any, loc string) any {
			return strings.HasPrefix(toString(a[0], loc), toString(a[1], loc))
		}},
		"hasSuffix": {2, 2, func(s *st, a []any, loc string) any {
			return strings.HasSuffix(toString(a[0], loc), toString(a[1], loc))
		}},
		"contains": {2, 2, func(s *st, a []any, loc string) any {
			return strings.Contains(toString(a[0], loc), toString(a[1], loc))
		}},
		"replace": {3, 3, func(s *st, a []any, loc string) any {
			return strings.ReplaceAll(toString(a[0], loc), toString(a[1], loc), toString(a[2], loc))
		}},
		"replaceRE": {3, 3, func(s *st, a []any, loc string) any {
			return regex(toString(a[0], loc), loc).ReplaceAllString(toString(a[2], loc), toString(a[1], loc))
		}},
		"findRE": {2, 2, func(s *st, a []any, loc string) any {
			var out []any
			for _, m := range regex(toString(a[0], loc), loc).FindAllString(toString(a[1], loc), -1) {
				out = append(out, m)
			}
			return out
		}},
		"findRESubmatch": {2, 2, func(s *st, a []any, loc string) any {
			var out []any
			for _, m := range regex(toString(a[0], loc), loc).FindAllStringSubmatch(toString(a[1], loc), -1) {
				groups := make([]any, len(m))
				for i, g := range m {
					groups[i] = g
				}
				out = append(out, groups)
			}
			return out
		}},
		"htmlUnescape": {1, 1, func(s *st, a []any, loc string) any { return html.UnescapeString(toString(a[0], loc)) }},
		"split": {2, 2, func(s *st, a []any, loc string) any {
			var out []any
			for _, p := range strings.Split(toString(a[0], loc), toString(a[1], loc)) {
				out = append(out, p)
			}
			return out
		}},
		"join": {2, 2, func(s *st, a []any, loc string) any {
			var parts []string
			for _, e := range asList(a[0], loc) {
				parts = append(parts, toString(e, loc))
			}
			return strings.Join(parts, toString(a[1], loc))
		}},
		"truncate": {2, 2, func(s *st, a []any, loc string) any {
			return truncate(toString(a[1], loc), toInt(a[0], loc))
		}},
		"plainify":    {1, 1, func(s *st, a []any, loc string) any { return plainify(toString(a[0], loc)) }},
		"slug":        {1, 1, func(s *st, a []any, loc string) any { return slug(toString(a[0], loc)) }},
		"markdownify": {1, 1, func(s *st, a []any, loc string) any { return s.r.markdownify(toString(a[0], loc), loc) }},
		"int":         {1, 1, func(s *st, a []any, loc string) any { return toInt(a[0], loc) }},
		"float":       {1, 1, func(s *st, a []any, loc string) any { return toFloat(a[0], loc) }},
		"string":      {1, 1, func(s *st, a []any, loc string) any { return toString(a[0], loc) }},
		"add": {2, 2, func(s *st, a []any, loc string) any {
			if x, ok := a[0].(string); ok {
				return x + toString(a[1], loc)
			}
			return num(func(x, y float64) float64 { return x + y }, func(x, y int) int { return x + y })(s, a, loc)
		}},
		"sub": {2, 2, num(func(x, y float64) float64 { return x - y }, func(x, y int) int { return x - y })},
		"mul": {2, 2, num(func(x, y float64) float64 { return x * y }, func(x, y int) int { return x * y })},
		"div": {2, 2, func(s *st, a []any, loc string) any {
			if toFloat(a[1], loc) == 0 {
				fail(loc, "division by zero")
			}
			return num(func(x, y float64) float64 { return x / y }, func(x, y int) int { return x / y })(s, a, loc)
		}},
		"mod": {2, 2, func(s *st, a []any, loc string) any {
			y := toInt(a[1], loc)
			if y == 0 {
				fail(loc, "division by zero")
			}
			return toInt(a[0], loc) % y
		}},
		"round":    {1, 1, func(s *st, a []any, loc string) any { return int(math.Round(toFloat(a[0], loc))) }},
		"floor":    {1, 1, func(s *st, a []any, loc string) any { return int(math.Floor(toFloat(a[0], loc))) }},
		"ceil":     {1, 1, func(s *st, a []any, loc string) any { return int(math.Ceil(toFloat(a[0], loc))) }},
		"safeHTML": {1, 1, func(s *st, a []any, loc string) any { return HTML(toString(a[0], loc)) }},
		"attr": {2, 2, func(s *st, a []any, loc string) any {
			name := toString(a[0], loc)
			for i := 0; i < len(name); i++ {
				if !isNameByte(name[i]) {
					fail(loc, "attr: invalid attribute name %q", name)
				}
			}
			if strings.HasPrefix(strings.ToLower(name), "on") {
				fail(loc, "attr: inline event handlers are not allowed")
			}
			switch v := a[1].(type) {
			case bool:
				if v {
					return Attr(name)
				}
				return Attr("")
			case nil:
				return Attr("")
			}
			val := toString(a[1], loc)
			if isURLAttr(name) && !safeURL(val) {
				fail(loc, "unsafe URL %q", val)
			}
			return Attr(name + `="` + string(escapeHTML(nil, val)) + `"`)
		}},
		"json": {1, 1, func(s *st, a []any, loc string) any { return JSON(appendJSON(nil, a[0], loc)) }},
		"absURL": {1, 1, func(s *st, a []any, loc string) any {
			u := toString(a[0], loc)
			if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
				return u
			}
			if !strings.HasPrefix(u, "/") {
				fail(loc, "absURL needs a path starting with /")
			}
			return s.r.p.BaseURL + u
		}},
		"link": {1, 1, func(s *st, a []any, loc string) any { return s.r.link(toString(a[0], loc), loc) }},
		"year": {0, 0, func(s *st, a []any, loc string) any {
			s.r.rec.year = true
			return time.Now().Year()
		}},
		"urlquery": {1, 1, func(s *st, a []any, loc string) any { return queryEscape(toString(a[0], loc)) }},
		"fail": {1, -1, func(s *st, a []any, loc string) any {
			args := make([]any, len(a)-1)
			for i, x := range a[1:] {
				args[i] = toString(x, loc)
			}
			fail(loc, toString(a[0], loc), args...)
			return nil
		}},
	}
}

func queryEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func truncate(text string, length int) string {
	var lastWordIndex, lastNonSpace, currentLen, nextTag int
	for i, r := range text {
		if i < nextTag {
			continue
		}
		if r == '<' {
			if j := strings.IndexByte(text[i:], '>'); j >= 0 {
				nextTag = i + j + 1
				continue
			}
		}
		currentLen++
		switch {
		case unicode.IsSpace(r):
			lastWordIndex = lastNonSpace
		case unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana):
			lastWordIndex = i
		default:
			lastNonSpace = i + utf8.RuneLen(r)
		}
		if currentLen > length {
			end := lastWordIndex
			if end == 0 {
				end = i
			}
			return text[:end] + " …"
		}
	}
	return text
}
