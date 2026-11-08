package yamlite

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

type Map struct {
	Keys   []string
	Vals   []any
	Lines  []int
	ID     int32
	Path   string
	read   []atomic.Bool
	index  map[string]int
	OnRead func(m *Map, i int)
}

func NewMap() *Map { return &Map{ID: -1} }

func (m *Map) Len() int { return len(m.Keys) }

func (m *Map) Find(key string) int {
	if m.index != nil {
		if i, ok := m.index[key]; ok {
			return i
		}
		return -1
	}
	for i, k := range m.Keys {
		if k == key {
			return i
		}
	}
	return -1
}

func (m *Map) Get(key string) (any, bool) {
	i := m.Find(key)
	if i < 0 {
		return nil, false
	}
	m.MarkRead(i)
	return m.Vals[i], true
}

func (m *Map) Peek(key string) (any, bool) {
	i := m.Find(key)
	if i < 0 {
		return nil, false
	}
	return m.Vals[i], true
}

func (m *Map) MarkRead(i int) {
	if m.read != nil && !m.read[i].Load() {
		m.read[i].Store(true)
	}
	if m.OnRead != nil {
		m.OnRead(m, i)
	}
}

func (m *Map) WasRead(i int) bool { return m.read != nil && m.read[i].Load() }

func (m *Map) Track() { m.read = make([]atomic.Bool, len(m.Keys)) }

func (m *Map) Set(key string, v any, line int) bool {
	if m.Find(key) >= 0 {
		return false
	}
	m.Keys = append(m.Keys, key)
	m.Vals = append(m.Vals, v)
	m.Lines = append(m.Lines, line)
	if m.index != nil {
		m.index[key] = len(m.Keys) - 1
	} else if len(m.Keys) > 8 {
		m.index = make(map[string]int, len(m.Keys)*2)
		for i, k := range m.Keys {
			m.index[k] = i
		}
	}
	return true
}

func Walk(v any, fn func(*Map)) {
	switch t := v.(type) {
	case *Map:
		fn(t)
		for _, c := range t.Vals {
			Walk(c, fn)
		}
	case []any:
		for _, c := range t {
			Walk(c, fn)
		}
	}
}

type Error struct {
	File string
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg) }

type line struct {
	indent int
	text   string
	num    int
}

type parser struct {
	file  string
	lines []line
	pos   int
	raw   []string
	first int
}

func Parse(file string, src []byte, firstLine int) (*Map, error) {
	p := &parser{file: file, first: firstLine}
	if !utf8.Valid(src) {
		return nil, &Error{file, firstLine, "document is not valid UTF-8"}
	}
	text := string(src)
	if strings.HasPrefix(text, "\ufeff") {
		return nil, &Error{file, firstLine, "byte order mark is not allowed"}
	}
	p.raw = strings.Split(text, "\n")
	for i, r := range p.raw {
		r = strings.TrimSuffix(r, "\r")
		p.raw[i] = r
		n := firstLine + i
		trimmed := strings.TrimLeft(r, " ")
		if strings.HasPrefix(trimmed, "\t") || (len(trimmed) > 0 && strings.TrimSpace(trimmed) == "" && strings.Contains(trimmed, "\t")) {
			return nil, &Error{file, n, "tabs are not allowed for indentation"}
		}
		if strings.TrimSpace(r) == "" {
			continue
		}
		indent := len(r) - len(trimmed)
		trimmed = strings.TrimRight(trimmed, " \t")
		switch {
		case trimmed == "---" || trimmed == "...":
			return nil, &Error{file, n, "multiple documents are not allowed"}
		}
		p.lines = append(p.lines, line{indent, trimmed, n})
	}
	root := NewMap()
	if len(p.lines) == 0 {
		return root, nil
	}
	if strings.HasPrefix(p.lines[0].text, "#") {
		return nil, p.err(p.lines[0], "comments are not allowed")
	}
	if p.lines[0].indent != 0 {
		return nil, p.err(p.lines[0], "document must start at column 1")
	}
	if isListItem(p.lines[0].text) {
		return nil, p.err(p.lines[0], "document root must be a mapping")
	}
	m, err := p.parseMap(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, p.err(p.lines[p.pos], "unexpected indentation")
	}
	return m, nil
}

func (p *parser) err(l line, format string, args ...any) error {
	return &Error{p.file, l.num, fmt.Sprintf(format, args...)}
}

func isListItem(s string) bool { return s == "-" || strings.HasPrefix(s, "- ") }

func (p *parser) parseBlock(indent int) (any, error) {
	l := p.lines[p.pos]
	if isListItem(l.text) {
		return p.parseList(indent)
	}
	return p.parseMap(indent)
}

func (p *parser) parseMap(indent int) (*Map, error) {
	m := NewMap()
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if strings.HasPrefix(l.text, "#") {
			return nil, p.err(l, "comments are not allowed")
		}
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, p.err(l, "unexpected indentation")
		}
		if isListItem(l.text) {
			return nil, p.err(l, "list item where a key was expected")
		}
		key, rest, err := p.splitKey(l)
		if err != nil {
			return nil, err
		}
		p.pos++
		var v any
		switch {
		case rest == "":
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				v, err = p.parseBlock(p.lines[p.pos].indent)
			} else if p.pos < len(p.lines) && p.lines[p.pos].indent == indent && isListItem(p.lines[p.pos].text) {
				v, err = p.parseList(indent)
			} else {
				return nil, p.err(l, "key %q has no value; null is not allowed", key)
			}
		case rest == "|" || rest == "|-" || rest == ">" || rest == ">-":
			v, err = p.blockScalar(l, indent, rest)
		default:
			v, err = p.scalar(l, rest)
		}
		if err != nil {
			return nil, err
		}
		if !m.Set(key, v, l.num) {
			return nil, p.err(l, "duplicate key %q", key)
		}
	}
	return m, nil
}

func (p *parser) parseList(indent int) ([]any, error) {
	var out []any
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if strings.HasPrefix(l.text, "#") {
			return nil, p.err(l, "comments are not allowed")
		}
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, p.err(l, "unexpected indentation")
		}
		if !isListItem(l.text) {
			break
		}
		rest := strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " ")
		itemIndent := indent + (len(l.text) - len(rest))
		switch {
		case rest == "":
			p.pos++
			if p.pos >= len(p.lines) || p.lines[p.pos].indent <= indent {
				return nil, p.err(l, "empty list item; null is not allowed")
			}
			v, err := p.parseBlock(p.lines[p.pos].indent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		case isListItem(rest):
			return nil, p.err(l, "nested inline list items are not allowed")
		case p.looksLikeKey(rest):
			p.lines[p.pos] = line{itemIndent, rest, l.num}
			v, err := p.parseMap(itemIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		default:
			p.pos++
			var v any
			var err error
			if rest == "|" || rest == "|-" || rest == ">" || rest == ">-" {
				v, err = p.blockScalar(l, indent, rest)
			} else {
				v, err = p.scalar(l, rest)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	return out, nil
}

func (p *parser) looksLikeKey(s string) bool {
	if strings.HasPrefix(s, "\"") {
		end := closingQuote(s)
		return end > 0 && (strings.HasPrefix(s[end+1:], ": ") || s[end+1:] == ":")
	}
	if strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") {
		return false
	}
	return strings.Contains(s, ": ") || strings.HasSuffix(s, ":")
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func (p *parser) splitKey(l line) (string, string, error) {
	s := l.text
	if strings.HasPrefix(s, "'") {
		return "", "", p.err(l, "single-quoted strings are not allowed; use double quotes")
	}
	if strings.HasPrefix(s, "\"") {
		end := closingQuote(s)
		if end < 0 {
			return "", "", p.err(l, "unterminated quoted key")
		}
		key, err := p.unquote(l, s[:end+1])
		if err != nil {
			return "", "", err
		}
		after := s[end+1:]
		if after == ":" {
			return key, "", nil
		}
		if !strings.HasPrefix(after, ": ") {
			return "", "", p.err(l, "expected \": \" after key")
		}
		return key, strings.TrimLeft(after[2:], " "), nil
	}
	before, after, ok := strings.Cut(s, ": ")
	var key, rest string
	if !ok {
		if !strings.HasSuffix(s, ":") {
			return "", "", p.err(l, "expected \"key: value\"")
		}
		key = s[:len(s)-1]
	} else {
		key, rest = before, strings.TrimLeft(after, " ")
	}
	if key == "" || strings.ContainsAny(key[:1], "[]{}&*!|>%@`?") || strings.Contains(key, " #") {
		return "", "", p.err(l, "invalid key %q", key)
	}
	if key == "<<" {
		return "", "", p.err(l, "merge keys are not allowed")
	}
	return key, rest, nil
}

func (p *parser) blockScalar(l line, parentIndent int, style string) (string, error) {
	start := p.pos
	var block []line
	for p.pos < len(p.lines) && p.lines[p.pos].indent > parentIndent {
		block = append(block, p.lines[p.pos])
		p.pos++
	}
	if len(block) == 0 {
		return "", p.err(l, "empty block scalar")
	}
	base := block[0].indent
	first, last := p.lines[start].num, block[len(block)-1].num
	var rows []string
	for n := first; n <= last; n++ {
		raw := p.raw[n-p.first]
		if strings.TrimSpace(raw) == "" {
			rows = append(rows, "")
			continue
		}
		if len(raw)-len(strings.TrimLeft(raw, " ")) < base {
			return "", &Error{p.file, n, "block scalar line is less indented than its first line"}
		}
		rows = append(rows, strings.TrimRight(raw[base:], " "))
	}
	var out string
	if style[0] == '|' {
		out = strings.Join(rows, "\n")
	} else {
		var b strings.Builder
		for i, r := range rows {
			switch {
			case i == 0:
				b.WriteString(r)
			case r == "":
				b.WriteByte('\n')
			case rows[i-1] == "" || strings.HasPrefix(r, " ") || strings.HasPrefix(rows[i-1], " "):
				if rows[i-1] != "" {
					b.WriteByte('\n')
				}
				b.WriteString(r)
			default:
				b.WriteByte(' ')
				b.WriteString(r)
			}
		}
		out = b.String()
	}
	if !strings.HasSuffix(style, "-") {
		out += "\n"
	}
	return out, nil
}

func (p *parser) scalar(l line, s string) (any, error) {
	switch {
	case strings.HasPrefix(s, "\""):
		end := closingQuote(s)
		if end < 0 {
			return nil, p.err(l, "unterminated double-quoted string (multi-line quoted strings are not allowed; use a block scalar)")
		}
		if strings.TrimSpace(s[end+1:]) != "" {
			return nil, p.err(l, "unexpected text after quoted string")
		}
		return p.unquote(l, s[:end+1])
	case strings.HasPrefix(s, "'"):
		return nil, p.err(l, "single-quoted strings are not allowed; use double quotes")
	case strings.HasPrefix(s, "["):
		return p.flowSeq(l, s)
	case s == "{}":
		return NewMap(), nil
	case strings.HasPrefix(s, "{"):
		return nil, p.err(l, "flow mappings are not allowed; use a block mapping")
	case strings.HasPrefix(s, "&") || strings.HasPrefix(s, "*"):
		return nil, p.err(l, "anchors and aliases are not allowed")
	case strings.HasPrefix(s, "!"):
		return nil, p.err(l, "tags are not allowed")
	case s == "|+" || s == ">+":
		return nil, p.err(l, "keep-chomping block scalars are not allowed")
	}
	return p.plain(l, s)
}

func (p *parser) plain(l line, s string) (any, error) {
	if strings.ContainsAny(s[:1], "%@`|>?") {
		return nil, p.err(l, "plain value %q starts with a reserved character; quote it", s)
	}
	if strings.Contains(s, " #") {
		return nil, p.err(l, "comments are not allowed; quote the value if \" #\" is part of it")
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return nil, p.err(l, "plain value %q contains \": \" or ends with \":\"; quote it", s)
	}
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "~", "Null", "NULL":
		return nil, p.err(l, "null is not allowed; omit the key instead")
	}
	if isCanonicalInt(s) {
		n, err := strconv.Atoi(s)
		if err == nil {
			return n, nil
		}
	}
	if isCanonicalFloat(s) {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil {
			return f, nil
		}
	}
	return s, nil
}

func isCanonicalInt(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isCanonicalFloat(s string) bool {
	i := strings.IndexByte(s, '.')
	if i <= 0 || i == len(s)-1 {
		return false
	}
	if !isCanonicalInt(s[:i]) {
		return false
	}
	for _, c := range []byte(s[i+1:]) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (p *parser) flowSeq(l line, s string) ([]any, error) {
	if !strings.HasSuffix(s, "]") {
		return nil, p.err(l, "flow sequences must be on one line")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	out := []any{}
	if inner == "" {
		return out, nil
	}
	for len(inner) > 0 {
		var item string
		if inner[0] == '"' {
			end := closingQuote(inner)
			if end < 0 {
				return nil, p.err(l, "unterminated string in flow sequence")
			}
			v, err := p.unquote(l, inner[:end+1])
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			inner = strings.TrimSpace(inner[end+1:])
		} else {
			j := strings.IndexByte(inner, ',')
			if j < 0 {
				item, inner = strings.TrimSpace(inner), ""
			} else {
				item, inner = strings.TrimSpace(inner[:j]), inner[j:]
			}
			if item == "" || strings.ContainsAny(item, "[]{}") {
				return nil, p.err(l, "flow sequences may only contain scalars")
			}
			v, err := p.plain(l, item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if inner == "" {
			break
		}
		if inner[0] != ',' {
			return nil, p.err(l, "expected \",\" in flow sequence")
		}
		inner = strings.TrimSpace(inner[1:])
		if inner == "" {
			return nil, p.err(l, "trailing comma in flow sequence")
		}
	}
	return out, nil
}

func (p *parser) unquote(l line, s string) (string, error) {
	body := s[1 : len(s)-1]
	if !strings.Contains(body, "\\") {
		return body, nil
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", p.err(l, "dangling escape")
		}
		switch body[i] {
		case '"', '\\', '/':
			b.WriteByte(body[i])
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u', 'U', 'x':
			n := map[byte]int{'u': 4, 'U': 8, 'x': 2}[body[i]]
			if i+1+n > len(body) {
				return "", p.err(l, "short escape sequence")
			}
			r, err := strconv.ParseUint(body[i+1:i+1+n], 16, 32)
			if err != nil {
				return "", p.err(l, "invalid escape sequence")
			}
			b.WriteRune(rune(r))
			i += n
		default:
			return "", p.err(l, "unknown escape \\%c", body[i])
		}
	}
	return b.String(), nil
}

func SplitFrontMatter(src []byte) (fm, body []byte, ok bool) {
	s := src
	if len(s) >= 4 && string(s[:4]) == "---\n" {
		s = s[4:]
	} else if len(s) >= 5 && string(s[:5]) == "---\r\n" {
		s = s[5:]
	} else {
		return nil, src, false
	}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] != '\n' {
			j++
		}
		ln := strings.TrimRight(string(s[i:j]), "\r")
		if ln == "---" {
			end := j
			if end < len(s) {
				end++
			}
			return s[:i], s[end:], true
		}
		i = j + 1
	}
	return nil, src, false
}
