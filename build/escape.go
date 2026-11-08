package build

import (
	"bytes"
	"fmt"
	"strings"
)

type hstate uint8

const (
	sText hstate = iota
	sTag
	sAttrName
	sAfterName
	sBeforeValue
	sAttrDQ
	sAttrUnquoted
	sScript
	sStyle
	sPre
	sRaw
	sComment
)

type hctx struct {
	state   hstate
	tag     string
	closing bool
	attr    string
	lastTag string
	known   bool
}

func (c hctx) same(o hctx) bool {
	if c.inTag() && o.inTag() {
		return c.tag == o.tag
	}
	return c.state == o.state && c.tag == o.tag && c.attr == o.attr
}

func (c hctx) inTag() bool {
	return c.state == sTag || c.state == sAttrName || c.state == sAfterName
}

var blockTags = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`html head body title meta link script style noscript base div p section header footer nav main article aside
		ul ol li dl dt dd table thead tbody tfoot tr td th caption colgroup col form fieldset legend figure figcaption details summary
		h1 h2 h3 h4 h5 h6 hr br blockquote pre address dialog template option optgroup select svg g path defs symbol use circle rect line
		polyline polygon ellipse lineargradient radialgradient stop clippath mask iframe video audio source picture`) {
		blockTags[t] = true
	}
}

var voidTags = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}

func isURLAttr(a string) bool {
	switch a {
	case "href", "src", "action", "formaction", "poster", "cite", "srcset", "data", "xlink:href":
		return true
	}
	return false
}

func isNameByte(c byte) bool {
	return c == '-' || c == ':' || c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f' }

func processText(in []byte, c hctx, atStart bool) ([]byte, hctx, error) {
	out := make([]byte, 0, len(in))
	i := 0
	for i < len(in) {
		ch := in[i]
		switch c.state {
		case sText, sPre:
			if ch == '<' {
				if bytes.HasPrefix(in[i:], []byte("<!--")) {
					c.state = sComment
					i += 4
					continue
				}
				if bytes.HasPrefix(in[i:], []byte("<!")) {
					end := bytes.IndexByte(in[i:], '>')
					if end < 0 {
						return nil, c, fmt.Errorf("unterminated <! declaration")
					}
					out = append(out, in[i:i+end+1]...)
					i += end + 1
					c.lastTag, c.known = "!doctype", true
					continue
				}
				j := i + 1
				closing := false
				if j < len(in) && in[j] == '/' {
					closing = true
					j++
				}
				k := j
				for k < len(in) && isNameByte(in[k]) {
					k++
				}
				if k > j || (bytes.HasPrefix(in[i:], []byte("<!")) && !closing) {
					name := strings.ToLower(string(in[j:k]))
					if c.state == sPre && !(closing && name == c.tag) {
						out = append(out, ch)
						i++
						continue
					}
					if name == "" {
						name = "!doctype"
						k = i + 2
					}
					out = append(out, in[i:k]...)
					c = hctx{state: sTag, tag: name, closing: closing, lastTag: c.lastTag, known: c.known}
					i = k
					continue
				}
				out = append(out, ch)
				c.known = false
				i++
				continue
			}
			if isSpace(ch) && c.state == sText {
				j := i
				nl := false
				for j < len(in) && isSpace(in[j]) {
					if in[j] == '\n' {
						nl = true
					}
					j++
				}
				prevTag := ""
				if c.known && len(out) > 0 && out[len(out)-1] == '>' {
					prevTag = c.lastTag
				} else if c.known && len(out) == 0 && atStart && i == 0 {
					prevTag = c.lastTag
				}
				nextTag := ""
				nextIsTag := j < len(in) && in[j] == '<'
				if nextIsTag {
					k := j + 1
					if k < len(in) && in[k] == '/' {
						k++
					}
					e := k
					for e < len(in) && isNameByte(in[e]) {
						e++
					}
					nextTag = strings.ToLower(string(in[k:e]))
					if bytes.HasPrefix(in[j:], []byte("<!")) {
						nextTag = "!doctype"
					}
				}
				drop := false
				switch {
				case i == 0 && atStart && len(out) == 0:
					drop = true
				case blockTags[prevTag] || prevTag == "!doctype":
					drop = nextIsTag || nl || j == len(in)
				case nextIsTag && (blockTags[nextTag] || nextTag == "!doctype"):
					drop = true
				case j == len(in) && nl && prevTag != "":
					drop = blockTags[prevTag]
				}
				if !drop {
					out = append(out, ' ')
				}
				i = j
				continue
			}
			out = append(out, ch)
			if c.state == sText {
				c.known = false
			}
			i++
		case sComment:
			end := bytes.Index(in[i:], []byte("-->"))
			if end < 0 {
				i = len(in)
				continue
			}
			i += end + 3
			c.state = sText
		case sTag, sAfterName:
			switch {
			case ch == '>':
				out = append(out, ch)
				i++
				c = endTag(c)
			case ch == '/' && i+1 < len(in) && in[i+1] == '>':
				out = append(out, '/', '>')
				i += 2
				c = endTag(c)
				if c.state != sText {
					c.state = sText
				}
			case isSpace(ch):
				j := i
				for j < len(in) && isSpace(in[j]) {
					j++
				}
				if (j == len(in) || (in[j] != '>' && !(in[j] == '/' && j+1 < len(in) && in[j+1] == '>'))) && (len(out) == 0 || out[len(out)-1] != ' ') {
					out = append(out, ' ')
				}
				i = j
			case ch == '=' && c.state == sAfterName:
				out = append(out, ch)
				c.state = sBeforeValue
				i++
			default:
				if c.state == sAfterName {
					c.state = sTag
				}
				j := i
				for j < len(in) && !isSpace(in[j]) && in[j] != '=' && in[j] != '>' && !(in[j] == '/' && j+1 < len(in) && in[j+1] == '>') {
					j++
				}
				if j == i {
					return nil, c, fmt.Errorf("unexpected %q inside <%s>", ch, c.tag)
				}
				if len(out) > 0 && out[len(out)-1] != ' ' && out[len(out)-1] != '"' {
					return nil, c, fmt.Errorf("attribute must be separated by a space inside <%s>", c.tag)
				}
				c.attr = strings.ToLower(string(in[i:j]))
				if strings.HasPrefix(c.attr, "on") {
					return nil, c, fmt.Errorf("inline event handler %s= is not allowed; use a script asset", c.attr)
				}
				out = append(out, in[i:j]...)
				i = j
				c.state = sAttrName
			}
		case sAttrName:
			switch {
			case ch == '=':
				out = append(out, ch)
				c.state = sBeforeValue
				i++
			case isSpace(ch):
				c.state = sAfterName
			default:
				c.state = sTag
			}
		case sBeforeValue:
			switch {
			case ch == '"':
				out = append(out, ch)
				c.state = sAttrDQ
				i++
			case ch == '\'':
				return nil, c, fmt.Errorf("use double quotes for attribute %s in <%s>", c.attr, c.tag)
			case isSpace(ch):
				i++
			default:
				return nil, c, fmt.Errorf("attribute %s in <%s> must be double-quoted", c.attr, c.tag)
			}
		case sAttrDQ:
			end := bytes.IndexByte(in[i:], '"')
			if end < 0 {
				out = append(out, in[i:]...)
				i = len(in)
				continue
			}
			out = append(out, in[i:i+end+1]...)
			i += end + 1
			c.state = sTag
			c.attr = ""
		case sScript, sStyle, sRaw:
			closer := []byte("</" + c.tag)
			end := indexFold(in[i:], closer)
			if end < 0 {
				out = append(out, in[i:]...)
				i = len(in)
				continue
			}
			out = append(out, in[i:i+end]...)
			i += end
			c.state = sText
		}
	}
	return out, c, nil
}

func endTag(c hctx) hctx {
	tag := c.tag
	n := hctx{state: sText, lastTag: tag, known: true}
	if c.closing || voidTags[tag] {
		return n
	}
	switch tag {
	case "script":
		n.state, n.tag = sScript, tag
	case "style":
		n.state, n.tag = sStyle, tag
	case "pre":
		n.state, n.tag = sPre, tag
	case "textarea", "title":
		n.state, n.tag = sRaw, tag
	}
	return n
}

func indexFold(s, sub []byte) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if bytes.EqualFold(s[i:i+n], sub) {
			return i
		}
	}
	return -1
}

type outMode uint8

const (
	outText outMode = iota
	outAttr
	outURL
	outStyleAttr
	outTagAttrs
	outScript
	outStyle
	outRaw
)

func modeFor(c hctx) (outMode, error) {
	switch c.state {
	case sText, sPre:
		return outText, nil
	case sRaw:
		return outRaw, nil
	case sTag, sAfterName, sAttrName:
		return outTagAttrs, nil
	case sAttrDQ:
		switch {
		case isURLAttr(c.attr):
			return outURL, nil
		case c.attr == "style":
			return outStyleAttr, nil
		}
		return outAttr, nil
	case sBeforeValue, sAttrUnquoted:
		return 0, fmt.Errorf("attribute %s in <%s> must be double-quoted around template output", c.attr, c.tag)
	case sScript:
		return outScript, nil
	case sStyle:
		return outStyle, nil
	case sComment:
		return 0, fmt.Errorf("template output inside an HTML comment is not allowed")
	}
	return 0, fmt.Errorf("unknown context")
}

func escapeHTML(dst []byte, s string) []byte {
	last := 0
	for i := 0; i < len(s); i++ {
		var rep string
		switch s[i] {
		case '&':
			rep = "&amp;"
		case '<':
			rep = "&lt;"
		case '>':
			rep = "&gt;"
		case '"':
			rep = "&#34;"
		case '\'':
			rep = "&#39;"
		default:
			continue
		}
		dst = append(dst, s[last:i]...)
		dst = append(dst, rep...)
		last = i + 1
	}
	return append(dst, s[last:]...)
}

func escapeText(dst []byte, s string) []byte {
	last := 0
	for i := 0; i < len(s); i++ {
		var rep string
		switch s[i] {
		case '&':
			rep = "&amp;"
		case '<':
			rep = "&lt;"
		case '>':
			rep = "&gt;"
		default:
			continue
		}
		dst = append(dst, s[last:i]...)
		dst = append(dst, rep...)
		last = i + 1
	}
	return append(dst, s[last:]...)
}

func safeCSSValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte(" #.,%()-_:;/+*!", c) >= 0:
		case c >= 0x80:
		default:
			return false
		}
	}
	return !strings.Contains(strings.ToLower(s), "url(") && !strings.Contains(strings.ToLower(s), "expression")
}

func safeURL(s string) bool {
	i := strings.IndexAny(s, ":/?#")
	if i < 0 || s[i] != ':' {
		return true
	}
	switch strings.ToLower(s[:i]) {
	case "https", "mailto", "tel", "http", "sms":
		return true
	}
	return false
}
