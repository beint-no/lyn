package build

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
	"github.com/zeebo/xxh3"
)

var inputTypes = setOf("button checkbox color date datetime-local email file hidden image month number password radio range reset search submit tel text time url week")

var linkRels = setOf(`alternate author canonical compression-dictionary dns-prefetch expect help icon license manifest me modulepreload
	next pingback preconnect prefetch preload prev privacy-policy search stylesheet terms-of-service apple-touch-icon
	apple-touch-icon-precomposed mask-icon webmention shortcut`)

var anchorRels = setOf(`alternate author bookmark external help license me next nofollow noopener noreferrer opener prev privacy-policy
	search sponsored tag terms-of-service ugc`)

var autofillFields = setOf(`name honorific-prefix given-name additional-name family-name honorific-suffix nickname username new-password
	current-password one-time-code organization-title organization street-address address-line1 address-line2 address-line3
	address-level4 address-level3 address-level2 address-level1 country country-name postal-code cc-name cc-given-name
	cc-additional-name cc-family-name cc-number cc-exp cc-exp-month cc-exp-year cc-csc cc-type transaction-currency
	transaction-amount language bday bday-day bday-month bday-year sex url photo tel tel-country-code tel-national
	tel-area-code tel-local tel-local-prefix tel-local-suffix tel-extension email impp`)

var contactFields = setOf("tel tel-country-code tel-national tel-area-code tel-local tel-local-prefix tel-local-suffix tel-extension email impp")

var enumAttrs = map[string]map[string]bool{
	"loading":             setOf("lazy eager"),
	"decoding":            setOf("sync async auto"),
	"fetchpriority":       setOf("high low auto"),
	"inputmode":           setOf("none text decimal numeric tel search email url"),
	"enterkeyhint":        setOf("enter done go next previous search send"),
	"crossorigin":         setOf("_ anonymous use-credentials"),
	"referrerpolicy":      setOf("_ no-referrer no-referrer-when-downgrade same-origin origin strict-origin origin-when-cross-origin strict-origin-when-cross-origin unsafe-url"),
	"popover":             setOf("_ auto manual hint"),
	"popovertargetaction": setOf("toggle show hide"),
	"dir":                 setOf("ltr rtl auto"),
	"translate":           setOf("_ yes no"),
	"spellcheck":          setOf("_ true false"),
	"hidden":              setOf("_ hidden until-found"),
	"draggable":           setOf("true false"),
	"contenteditable":     setOf("_ true false plaintext-only"),
	"autocapitalize":      setOf("none off sentences on words characters"),
	"autocorrect":         setOf("_ on off"),
	"writingsuggestions":  setOf("_ true false"),
	"preload":             setOf("_ none metadata auto"),
	"wrap":                setOf("soft hard"),
	"kind":                setOf("subtitles captions descriptions chapters metadata"),
	"scope":               setOf("row col rowgroup colgroup"),
	"shape":               setOf("circle default poly rect"),
	"closedby":            setOf("any closerequest none"),
	"shadowrootmode":      setOf("open closed"),
}

var elemEnumAttrs = map[string]map[string]map[string]bool{
	"button": {"type": setOf("submit reset button"), "formmethod": setOf("get post dialog"), "formenctype": setOf("application/x-www-form-urlencoded multipart/form-data text/plain")},
	"input":  {"type": inputTypes, "formmethod": setOf("get post dialog"), "formenctype": setOf("application/x-www-form-urlencoded multipart/form-data text/plain")},
	"form":   {"method": setOf("get post dialog"), "enctype": setOf("application/x-www-form-urlencoded multipart/form-data text/plain"), "autocomplete": setOf("on off")},
	"ol":     {"type": setOf("1 a A i I")},
	"link":   {"as": setOf("fetch audio audioworklet document embed font image json manifest object paintworklet report script serviceworker sharedworker style track video worker xslt")},
	"meta":   {"charset": setOf("utf-8")},
}

func setOf(s string) map[string]bool {
	m := map[string]bool{}
	for f := range strings.FieldsSeq(s) {
		if f == "_" {
			f = ""
		}
		m[f] = true
	}
	return m
}

type tagLint struct {
	errf  func(off int, words []string, format string, args ...any)
	at    int
	tag   []byte
	id    int
	name  string
	attrs []attr
}

func attrWords(a attr) []string {
	if a.val == "" {
		return []string{a.name}
	}
	return []string{a.name + `="` + a.val + `"`, a.val}
}

func (t *tagLint) bad(a attr, format string, args ...any) {
	t.errf(t.at+attrOffset(t.tag, a.name), attrWords(a), format, args...)
}

func lintAttrs(t *tagLint) {
	if t.id < 0 {
		if strings.IndexByte(t.name, '-') <= 0 {
			t.errf(t.at, []string{"<" + t.name}, "unknown element <%s>%s; use a standard HTML element or a custom element name with a hyphen", t.name, suggest(t.name, elemTable.names))
		}
		return
	}
	for i, a := range t.attrs {
		for _, prev := range t.attrs[:i] {
			if prev.name == a.name {
				t.bad(a, "<%s> has the attribute %s twice; browsers use only the first, so keep one", t.name, a.name)
			}
		}
		if !knownAttr(t.id, int(a.id)) {
			if !strings.HasPrefix(a.name, "data-") && !strings.HasPrefix(a.name, "aria-") && !strings.HasPrefix(a.name, "on") {
				t.bad(a, "unknown attribute %s on <%s>%s; browsers ignore it", a.name, t.name, suggestAttr(a.name, t.id))
			}
			continue
		}
		lintValue(t, a)
	}
}

func suggestAttr(name string, e int) string {
	var cands []string
	for id, n := range attrTable.names {
		if knownAttr(e, id) {
			cands = append(cands, n)
		}
	}
	return suggest(name, cands)
}

func lintValue(t *tagLint, a attr) {
	v := a.val
	info := &attrInfos[a.id]
	if info.boolean {
		if v != "" && !strings.EqualFold(v, a.name) {
			t.bad(a, `boolean attribute %s="%s" is on whatever its value is; write just %s, or leave it out to turn it off`, a.name, v, a.name)
		}
		return
	}
	allowed := info.enum
	if info.perElement {
		if e := elemEnumAttrs[t.name]; e != nil && e[a.name] != nil {
			allowed = e[a.name]
		}
	}
	if allowed != nil {
		lv := strings.ToLower(v)
		if t.name == "ol" {
			lv = v
		}
		if !allowed[lv] {
			t.bad(a, "invalid %s=%q on <%s>; use %s%s", a.name, v, t.name, valueList(allowed), suggest(lv, keysOf(allowed)))
		}
		return
	}
	switch a.name {
	case "rel":
		if t.name == "link" || t.name == "a" || t.name == "area" || t.name == "form" {
			lintRel(t, a)
		}
	case "target", "formtarget":
		lintTarget(t, a)
	case "autocomplete":
		if t.name == "input" || t.name == "textarea" || t.name == "select" {
			if msg := autocompleteProblem(v); msg != "" {
				t.bad(a, "invalid autocomplete=%q: %s", v, msg)
			}
		}
	case "id":
		if v == "" || strings.ContainsAny(v, " \t\n\r\f") {
			t.bad(a, "id=%q must be non-empty and contain no spaces", v)
		}
	case "lang", "hreflang", "srclang":
		if !(a.name == "hreflang" && v == "x-default") && !(a.name == "lang" && v == "") && !validLangTag(v) {
			t.bad(a, "%s=%q is not a language tag; write it like nb, en or nb-NO", a.name, v)
		}
	case "tabindex":
		n, err := strconv.Atoi(v)
		if err != nil {
			t.bad(a, "tabindex=%q must be an integer", v)
		} else if n > 0 {
			t.bad(a, "tabindex=%q takes the element out of document order; use 0 or -1 and order the markup instead", v)
		}
	case "colspan", "rowspan", "span", "rows", "cols", "maxlength", "minlength", "size", "start":
		if n, err := strconv.Atoi(v); err != nil || n < 0 || a.name != "start" && a.name != "maxlength" && a.name != "minlength" && n == 0 {
			t.bad(a, "%s=%q must be a positive integer", a.name, v)
		}
	case "width", "height":
		if t.name != "svg" {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				t.bad(a, "%s=%q on <%s> must be a whole number of CSS pixels without a unit; set other sizes in CSS", a.name, v, t.name)
			}
		}
	}
}

func valueList(m map[string]bool) string {
	vals := keysOf(m)
	slices.Sort(vals)
	for i, v := range vals {
		if v == "" {
			vals[i] = `""`
		}
	}
	if len(vals) > 8 {
		return "a valid value"
	}
	return "one of " + strings.Join(vals, ", ")
}

func lintRel(t *tagLint, a attr) {
	allowed := anchorRels
	if t.name == "link" {
		allowed = linkRels
	}
	for tok := range strings.FieldsSeq(strings.ToLower(a.val)) {
		if !allowed[tok] {
			t.bad(a, "unknown rel value %q on <%s>%s", tok, t.name, suggest(tok, keysOf(allowed)))
		}
	}
}

func lintTarget(t *tagLint, a attr) {
	v := a.val
	switch strings.ToLower(v) {
	case "_blank", "_self", "_parent", "_top":
		if v != strings.ToLower(v) {
			t.bad(a, "%s=%q must be lowercase (%s)", a.name, v, strings.ToLower(v))
		}
		return
	case "blank", "self", "parent", "top", "new", "_new":
		t.bad(a, "%s=%q opens a window named %q, not a new tab each time; use _blank", a.name, v, v)
		return
	}
	if v == "" || strings.HasPrefix(v, "_") {
		t.bad(a, "invalid %s=%q; use _blank, _self, _parent, _top or a window name%s", a.name, v, suggest(v, []string{"_blank", "_self", "_parent", "_top"}))
	}
}

func autocompleteProblem(v string) string {
	toks := strings.Fields(strings.ToLower(v))
	if len(toks) == 0 {
		return "write on, off or an autofill field name"
	}
	if len(toks) == 1 && (toks[0] == "on" || toks[0] == "off") {
		return ""
	}
	i := 0
	if strings.HasPrefix(toks[i], "section-") {
		i++
	}
	if i < len(toks) && (toks[i] == "shipping" || toks[i] == "billing") {
		i++
	}
	contact := false
	if i < len(toks) {
		switch toks[i] {
		case "home", "work", "mobile", "fax", "pager":
			contact = true
			i++
		}
	}
	if i >= len(toks) {
		return "missing the field name"
	}
	field := toks[i]
	if !autofillFields[field] {
		return fmt.Sprintf("unknown field %q%s", field, suggest(field, keysOf(autofillFields)))
	}
	if contact && !contactFields[field] {
		return fmt.Sprintf("%q can only qualify tel, email or impp fields", toks[i-1])
	}
	i++
	if i < len(toks) && toks[i] == "webauthn" {
		i++
	}
	if i < len(toks) {
		return fmt.Sprintf("unexpected %q after the field name", toks[i])
	}
	return ""
}

func validLangTag(v string) bool {
	parts := strings.Split(v, "-")
	if len(parts[0]) < 2 || len(parts[0]) > 8 {
		return false
	}
	for i, p := range parts {
		if p == "" || len(p) > 8 {
			return false
		}
		for j := range len(p) {
			c := p[j]
			letter := c|0x20 >= 'a' && c|0x20 <= 'z'
			if !letter && (i == 0 || c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

type openTag struct {
	name  string
	off   int
	kids  int
	id    int
	flags uint16
}

func contentModel(id int, name string, stack []openTag, hiddenInput bool, pOpen, linkOpen int) string {
	if len(stack) == 0 {
		return ""
	}
	parent := &stack[len(stack)-1]
	if parent.name == "template" {
		return ""
	}
	if parent.id >= 0 && !elems[parent.id].children.empty() && !elems[parent.id].children.has(id) {
		return fmt.Sprintf("<%s> cannot be a child of <%s>; browsers move or drop it, so wrap it in %s", name, parent.name, childHint(parent.name))
	}
	if id < 0 {
		return ""
	}
	e := &elems[id]
	if !e.parents.empty() {
		good := e.parents.has(parent.id)
		if good && parent.name == "div" && (len(stack) < 2 || stack[len(stack)-2].name != "dl" && stack[len(stack)-2].name != "template") {
			good = false
		}
		if !good {
			return fmt.Sprintf("<%s> must be inside %s, not <%s>", name, e.parentNames, parent.name)
		}
		if (name == "summary" || name == "legend") && parent.kids > 0 {
			return fmt.Sprintf("<%s> must be the first child of <%s>; move it to the top", name, parent.name)
		}
	}
	if e.flags&eBlock != 0 {
		if pOpen > 0 {
			for _, s := range slices.Backward(stack) {
				if s.name == "p" {
					return fmt.Sprintf("<%s> inside <p>; browsers end the paragraph before it and leave an empty <p> after it, so use a <div> instead of the <p> or move the <%s> out", name, name)
				}
				if s.flags&eScope != 0 {
					break
				}
			}
		}
		for _, s := range slices.Backward(stack) {
			if s.flags&eTransparent != 0 {
				continue
			}
			if s.flags&ePhrasingOnly != 0 {
				return fmt.Sprintf("<%s> inside <%s>, which may only contain text-level elements; use a <span> or change the <%s> to a <div>", name, s.name, name)
			}
			break
		}
	}
	if name == "form" {
		for _, o := range stack {
			if o.name == "form" {
				return "<form> inside <form>; browsers ignore the inner form tag, so its fields submit with the outer form"
			}
		}
	}
	if linkOpen > 0 && e.flags&eInteractive != 0 && !hiddenInput {
		for _, s := range slices.Backward(stack) {
			if n := s.name; n == "a" || n == "button" {
				return fmt.Sprintf("<%s> inside <%s>; interactive elements cannot be nested, so clicks go to the wrong element (browsers even split nested links); move it out", name, n)
			}
		}
	}
	return ""
}

func childHint(parent string) string {
	switch parent {
	case "ul", "ol", "menu":
		return "<li>"
	case "dl":
		return "<dt> or <dd>"
	case "tr":
		return "<td> or <th>"
	case "colgroup":
		return "<col>"
	}
	return "<tr> and <td>"
}

var textTrigger = func() (t [256]bool) {
	for _, c := range []byte("{}]*L$\\|[&TFl#") {
		t[c] = true
	}
	return
}()

func wordAt[S ~string | ~[]byte](t S, i int, w string) bool {
	if !hasPrefixFoldBytes(t[i:], w, w[0] >= 'A' && w[0] <= 'Z') {
		return false
	}
	if i > 0 && isWordByte(t[i-1]) {
		return false
	}
	end := i + len(w)
	return end >= len(t) || !isWordByte(t[end])
}

func hasPrefixFoldBytes[S ~string | ~[]byte](b S, s string, exact bool) bool {
	if len(b) < len(s) {
		return false
	}
	if exact {
		return string(b[:len(s)]) == s
	}
	return strings.EqualFold(string(b[:len(s)]), s)
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z' || c >= 0x80
}

func textProblem[S ~string | ~[]byte](t S) (int, string, string) {
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !textTrigger[c] {
			continue
		}
		rest := t[i:]
		switch c {
		case '{':
			if len(rest) > 1 && rest[1] == '{' {
				return i, "{{", ""
			}
		case '}':
			if len(rest) > 1 && rest[1] == '}' {
				return i, "}}", ""
			}
		case ']':
			if len(rest) > 1 && (rest[1] == '(' || rest[1] == '[') {
				return i, string(rest[:2]), ""
			}
		case '*':
			if len(rest) > 1 && rest[1] == '*' {
				return i, "**", ""
			}
		case '$':
			if len(rest) > 1 && rest[1] == '$' {
				return i, "$$", ""
			}
		case 'L':
			if hasPrefixFoldBytes(rest, "LYNSC", true) {
				return i, "LYNSC", ""
			}
			if hasPrefixFoldBytes(rest, "Lorem ipsum", false) {
				return i, "", "placeholder text"
			}
		case 'l':
			if hasPrefixFoldBytes(rest, "lorem ipsum", false) {
				return i, "", "placeholder text"
			}
		case '\\':
			if hasPrefixFoldBytes(rest, "\\text{", true) || hasPrefixFoldBytes(rest, "\\frac{", true) {
				return i, string(rest[:5]), ""
			}
		case '|':
			j := 1
			for j < len(rest) && rest[j] == ' ' {
				j++
			}
			if j < len(rest) && rest[j] == ':' {
				j++
			}
			if j+1 < len(rest) && rest[j] == '-' && rest[j+1] == '-' {
				return i, "|--", ""
			}
		case '[':
			if len(rest) > 1 && rest[1] == '^' {
				return i, "[^", ""
			}
			if hasPrefixFoldBytes(rest, "[insert", false) {
				return i, "", "placeholder text"
			}
		case '&':
			if n := literalEntity(rest); n > 0 {
				return i, string(rest[:n]), "entity"
			}
		case 'T':
			if wordAt(t, i, "TODO") || wordAt(t, i, "TBD") {
				return i, "", "placeholder text"
			}
		case 'F':
			if wordAt(t, i, "FIXME") {
				return i, "", "placeholder text"
			}
		case '#':
			if (i == 0 || t[i-1] == '\n') && len(rest) > 2 && rest[1] == '#' {
				j := 2
				for j < len(rest) && j < 6 && rest[j] == '#' {
					j++
				}
				if j < len(rest) && rest[j] == ' ' {
					return i, string(rest[:j+1]), ""
				}
			}
		}
	}
	return -1, "", ""
}

func literalEntity[S ~string | ~[]byte](b S) int {
	if !hasPrefixFoldBytes(b, "&amp;", true) {
		return 0
	}
	j := 5
	if j < len(b) && b[j] == '#' {
		j++
	}
	start := j
	for j < len(b) && j-start < 10 && (b[j]|0x20 >= 'a' && b[j]|0x20 <= 'z' || b[j] >= '0' && b[j] <= '9') {
		j++
	}
	if j-start >= 2 && j < len(b) && b[j] == ';' {
		return j + 1
	}
	return 0
}

func mojibake(b []byte) int {
	for from := 0; from < len(b); {
		off := mojibakeFrom(b, from)
		if off < 0 || !insideCode(b[:off]) {
			return off
		}
		from = off + 1
	}
	return -1
}

func mojibakeFrom(b []byte, from int) int {
	end := len(b)
	if i := bytes.Index(b[from:], []byte("\uFFFD")); i >= 0 {
		end = from + i
	}
	for i := from; i < end; {
		j := bytes.IndexByte(b[i:end], 0xC3)
		if j < 0 {
			break
		}
		i += j
		r := b[i:]
		switch {
		case len(r) >= 3 && r[1] == 0x83 && r[2] >= 0x80:
			return i
		case len(r) >= 4 && r[1] == 0x82 && r[2] == 0xC2 && r[3] >= 0x80 && r[3] <= 0xBF:
			return i
		case len(r) >= 5 && r[1] == 0xA2 && r[2] == 0xE2 && r[3] == 0x82 && r[4] == 0xAC:
			return i
		}
		i += 2
	}
	if end < len(b) {
		return end
	}
	return -1
}

func insideCode(before []byte) bool {
	open := max(bytes.LastIndex(before, []byte("<code")), bytes.LastIndex(before, []byte("<pre")))
	closed := max(bytes.LastIndex(before, []byte("</code>")), bytes.LastIndex(before, []byte("</pre>")))
	return open > closed
}

func srcsetProblem(srcset, sizes string) string {
	if srcset == "" {
		return "sizes without srcset has no effect; add srcset or remove sizes"
	}
	if strings.Contains(srcset, "data:") {
		return ""
	}
	seen := map[string]bool{}
	widths, densities := false, false
	for cand := range strings.SplitSeq(srcset, ",") {
		f := strings.Fields(cand)
		switch len(f) {
		case 0:
			return fmt.Sprintf("srcset %q has an empty candidate; remove the extra comma", srcset)
		case 1:
			f = append(f, "1x")
		case 2:
		default:
			return fmt.Sprintf("srcset candidate %q has more than one descriptor; write \"url 480w\" or \"url 2x\"", strings.TrimSpace(cand))
		}
		d := f[1]
		num := d[:len(d)-1]
		switch d[len(d)-1] {
		case 'w':
			if n, err := strconv.Atoi(num); err != nil || n <= 0 {
				return fmt.Sprintf("srcset width %q must be a positive whole number followed by w", d)
			}
			widths = true
		case 'x':
			if x, err := strconv.ParseFloat(num, 64); err != nil || x <= 0 {
				return fmt.Sprintf("srcset density %q must be a positive number followed by x", d)
			}
			densities = true
		default:
			return fmt.Sprintf("srcset descriptor %q must be a width like 480w or a density like 2x", d)
		}
		if seen[d] {
			return fmt.Sprintf("srcset has two candidates for %s; keep one", d)
		}
		seen[d] = true
	}
	if widths && densities {
		return "srcset mixes width (w) and density (x) descriptors; use one kind"
	}
	if widths && strings.TrimSpace(sizes) == "" {
		return "srcset with width descriptors needs a sizes attribute"
	}
	if sizes != "" {
		if !widths {
			return "sizes only applies to srcset width descriptors; remove sizes or use w descriptors"
		}
		return sizesProblem(sizes)
	}
	return ""
}

func sizesProblem(sizes string) string {
	entries := strings.Split(sizes, ",")
	for n, e := range entries {
		e = strings.TrimSpace(e)
		if e == "auto" && n == 0 {
			continue
		}
		cond, length := "", e
		if strings.HasPrefix(e, "(") || strings.HasPrefix(e, "not ") {
			depth, end := 0, -1
			for i, c := range e {
				if c == '(' {
					depth++
				} else if c == ')' {
					depth--
					if depth == 0 {
						end = i
					}
				}
			}
			if end < 0 {
				return fmt.Sprintf("sizes entry %q has an unclosed media condition", e)
			}
			cond, length = e[:end+1], strings.TrimSpace(e[end+1:])
		}
		if cond == "" && n < len(entries)-1 {
			return fmt.Sprintf("sizes entry %q has no media condition, so the entries after it are never used; put it last", e)
		}
		if !cssLength(length) {
			return fmt.Sprintf("sizes entry %q must end with a length such as 100vw or 640px", e)
		}
	}
	return ""
}

func cssLength(s string) bool {
	if s == "0" {
		return true
	}
	for _, f := range []string{"calc(", "min(", "max(", "clamp("} {
		if strings.HasPrefix(s, f) && strings.HasSuffix(s, ")") {
			return true
		}
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return false
	}
	switch s[i:] {
	case "px", "em", "rem", "vw", "vh", "vmin", "vmax", "ch", "ex", "svw", "lvw", "dvw", "svh", "lvh", "dvh", "cqw", "cqh", "cqi", "cqb", "cqmin", "cqmax", "vi", "vb", "cm", "mm", "in", "pt", "pc", "q", "lh", "rlh", "cap", "ic":
		return true
	}
	return false
}

type scriptResult struct {
	off int
	msg string
	ids []string
}

var scriptCache sync.Map

func checkScript(body []byte) scriptResult {
	key := xxh3.Hash(body)
	if v, ok := scriptCache.Load(key); ok {
		return v.(scriptResult)
	}
	var r scriptResult
	r.off, r.msg = jsSyntax(body)
	r.ids = scriptIDs(body)
	scriptCache.Store(key, r)
	return r
}

func jsSyntax(body []byte) (int, string) {
	if len(bytes.TrimSpace(body)) == 0 {
		return 0, ""
	}
	_, err := js.Parse(parse.NewInputBytes(bytes.Clone(body)), js.Options{})
	if err == nil {
		return 0, ""
	}
	pe, ok := err.(*parse.Error)
	if !ok {
		return 0, "JavaScript syntax error: " + err.Error()
	}
	off := 0
	for range pe.Line - 1 {
		nl := bytes.IndexByte(body[off:], '\n')
		if nl < 0 {
			break
		}
		off += nl + 1
	}
	off = min(len(body), off+max(0, pe.Column-1))
	return off, fmt.Sprintf("JavaScript syntax error: %s at %q", pe.Message, strings.TrimSpace(pe.Context))
}

func scriptIDs(b []byte) []string {
	var out []string
	for _, call := range []string{"getElementById(", "querySelector(", "querySelectorAll("} {
		rest := b
		for {
			i := bytes.Index(rest, []byte(call))
			if i < 0 {
				break
			}
			rest = rest[i+len(call):]
			if len(rest) < 3 || rest[0] != '"' && rest[0] != '\'' && rest[0] != '`' {
				continue
			}
			end := bytes.IndexByte(rest[1:], rest[0]) + 1
			if end <= 0 || end+1 >= len(rest) || rest[end+1] != ')' {
				continue
			}
			str := rest[1:end]
			if call != "getElementById(" {
				if len(str) < 2 || str[0] != '#' {
					continue
				}
				str = str[1:]
			}
			k := 0
			for k < len(str) && isPropertyByte(str[k]) {
				k++
			}
			if k == 0 || call == "getElementById(" && k != len(str) {
				continue
			}
			if k < len(str) && !strings.ContainsRune(" .[:>,~+", rune(str[k])) {
				continue
			}
			out = append(out, string(str[:k]))
		}
	}
	return out
}
