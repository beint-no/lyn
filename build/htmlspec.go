package build

import (
	"fmt"
	"strings"
)

type nameTable struct {
	slots [1024][]int32
	names []string
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func nameSlot[S ~string | ~[]byte](b S) int {
	n := len(b)
	return (n*131 + int(lowerByte(b[0]))*31 + int(lowerByte(b[n/2]))*7 + int(lowerByte(b[n-1]))) & 1023
}

func (t *nameTable) add(name string) int {
	if id := lookupName(t, name); id >= 0 {
		return id
	}
	id := len(t.names)
	t.names = append(t.names, name)
	h := nameSlot(name)
	t.slots[h] = append(t.slots[h], int32(id))
	return id
}

func lookupName[S ~string | ~[]byte](t *nameTable, b S) int {
	if len(b) == 0 {
		return -1
	}
	for _, id := range t.slots[nameSlot(b)] {
		name := t.names[id]
		if len(name) != len(b) {
			continue
		}
		same := true
		for i := range len(name) {
			if lowerByte(b[i]) != name[i] {
				same = false
				break
			}
		}
		if same {
			return int(id)
		}
	}
	return -1
}

type idSet [4]uint64

func (s *idSet) set(id int)      { s[id>>6] |= 1 << (id & 63) }
func (s *idSet) has(id int) bool { return id >= 0 && s[id>>6]&(1<<(id&63)) != 0 }
func (s *idSet) empty() bool     { return s[0]|s[1]|s[2]|s[3] == 0 }

const (
	eBlock uint16 = 1 << iota
	ePhrasingOnly
	eTransparent
	eScope
	eInteractive
	eTextless
	eVoid
	eForeign
)

type elemInfo struct {
	flags       uint16
	attrs       idSet
	parents     idSet
	children    idSet
	parentNames string
}

type attrInfo struct {
	global     bool
	boolean    bool
	idref      bool
	perElement bool
	enum       map[string]bool
}

var elemTable, attrTable nameTable
var elems []elemInfo
var attrInfos []attrInfo

func elemID(name string) int {
	id := elemTable.add(name)
	for len(elems) <= id {
		elems = append(elems, elemInfo{})
	}
	return id
}

func attrID(name string) int {
	id := attrTable.add(name)
	if id >= 256 {
		panic(fmt.Sprintf("too many attribute names (%s)", name))
	}
	for len(attrInfos) <= id {
		attrInfos = append(attrInfos, attrInfo{})
	}
	return id
}

func init() {
	flag := func(f uint16, names string) {
		for n := range strings.FieldsSeq(names) {
			elems[elemID(n)].flags |= f
		}
	}
	allow := func(names, attrs string) {
		for n := range strings.FieldsSeq(names) {
			e := elemID(n)
			for a := range strings.FieldsSeq(attrs) {
				elems[e].attrs.set(attrID(a))
			}
		}
	}
	flag(0, `html head body title base link meta style script noscript template slot main nav header footer section article
		aside address h1 h2 h3 h4 h5 h6 hgroup search p hr pre blockquote ol ul menu li dl dt dd figure figcaption div a em
		strong small s cite q dfn abbr ruby rt rp data time code var samp kbd sub sup i b u mark bdi bdo span br wbr ins del
		picture source img iframe embed object video audio track map area table caption colgroup col tbody thead tfoot tr
		td th form label input button select datalist optgroup option textarea output progress meter fieldset legend
		details summary dialog canvas svg math selectedcontent`)
	flag(eBlock, "address article aside blockquote details dialog div dl fieldset figcaption figure footer form header hgroup hr main menu nav ol p pre search section table ul h1 h2 h3 h4 h5 h6")
	flag(ePhrasingOnly, "span b strong em i small label button h1 h2 h3 h4 h5 h6 p pre summary legend abbr cite code dfn kbd mark q s samp sub sup time u var output data bdi bdo")
	flag(eTransparent, "a ins del map slot noscript object canvas audio video")
	flag(eScope, "button table td th caption template object html")
	flag(eInteractive, "a button select textarea details iframe embed input")
	flag(eTextless, "ul ol menu dl table thead tbody tfoot tr colgroup select")
	flag(eVoid, "area base br col embed hr img input link meta source track wbr")
	flag(eForeign, "svg math")
	parents := map[string]string{
		"li": "ul ol menu", "dt": "dl div", "dd": "dl div", "tr": "table thead tbody tfoot", "td": "tr", "th": "tr",
		"thead": "table", "tbody": "table", "tfoot": "table", "caption": "table", "colgroup": "table", "col": "colgroup",
		"option": "select datalist optgroup", "optgroup": "select", "summary": "details", "figcaption": "figure",
		"legend": "fieldset", "source": "picture video audio", "track": "video audio",
	}
	for child, list := range parents {
		e := elemID(child)
		var names []string
		for p := range strings.FieldsSeq(list + " template") {
			elems[e].parents.set(elemID(p))
			if p != "div" && p != "template" {
				names = append(names, "<"+p+">")
			}
		}
		elems[e].parentNames = strings.Join(names, " or ")
	}
	children := map[string]string{
		"ul": "li", "ol": "li", "menu": "li", "dl": "dt dd div", "table": "caption colgroup thead tbody tfoot tr",
		"thead": "tr", "tbody": "tr", "tfoot": "tr", "tr": "td th", "colgroup": "col",
	}
	for parent, list := range children {
		for c := range strings.FieldsSeq(list + " script template") {
			elems[elemID(parent)].children.set(elemID(c))
		}
	}
	for a := range strings.FieldsSeq(`accesskey autocapitalize autocorrect autofocus class contenteditable dir draggable
		enterkeyhint exportparts hidden id inert inputmode is itemid itemprop itemref itemscope itemtype lang nonce part
		popover role slot spellcheck style tabindex title translate writingsuggestions xml:lang xmlns`) {
		attrInfos[attrID(a)].global = true
	}
	allow("a", "href target download ping rel hreflang type referrerpolicy")
	allow("area", "alt coords shape href target download ping rel referrerpolicy")
	allow("audio", "src crossorigin preload autoplay loop muted controls controlslist")
	allow("base", "href target")
	allow("blockquote q", "cite")
	allow("button", "command commandfor disabled form formaction formenctype formmethod formnovalidate formtarget name popovertarget popovertargetaction type value")
	allow("canvas", "width height")
	allow("col colgroup", "span")
	allow("data", "value")
	allow("del ins", "cite datetime")
	allow("details", "open name")
	allow("dialog", "open closedby")
	allow("embed", "src type width height")
	allow("fieldset", "disabled form name")
	allow("form", "accept-charset action autocomplete enctype method name novalidate target rel")
	allow("html", "manifest")
	allow("iframe", "src srcdoc name sandbox allow allowfullscreen width height referrerpolicy loading")
	allow("img", "alt src srcset sizes crossorigin usemap ismap width height referrerpolicy decoding loading fetchpriority")
	allow("input", "accept alpha alt autocomplete checked colorspace dirname disabled form formaction formenctype formmethod formnovalidate formtarget height list max maxlength min minlength multiple name pattern placeholder popovertarget popovertargetaction readonly required size src step type value width capture")
	allow("label", "for")
	allow("li", "value")
	allow("link", "href crossorigin rel media integrity hreflang type referrerpolicy sizes imagesrcset imagesizes as blocking color disabled fetchpriority")
	allow("map", "name")
	allow("meta", "name http-equiv content charset media property")
	allow("meter", "value min max low high optimum")
	allow("object", "data type name form width height")
	allow("ol", "reversed start type")
	allow("optgroup", "disabled label")
	allow("option", "disabled label selected value")
	allow("output", "for form name")
	allow("progress", "value max")
	allow("script", "src type nomodule async defer crossorigin integrity referrerpolicy blocking fetchpriority")
	allow("select", "autocomplete disabled form multiple name required size")
	allow("slot", "name")
	allow("source", "type media src srcset sizes width height")
	allow("style", "media blocking")
	allow("td th", "colspan rowspan headers")
	allow("th", "scope abbr")
	allow("template", "shadowrootmode shadowrootdelegatesfocus shadowrootclonable shadowrootserializable")
	allow("textarea", "autocomplete cols dirname disabled form maxlength minlength name placeholder readonly required rows wrap")
	allow("time", "datetime")
	allow("track", "default kind label src srclang")
	allow("video", "src crossorigin poster preload autoplay playsinline loop muted controls width height controlslist disablepictureinpicture disableremoteplayback")
	for a := range strings.FieldsSeq(`allowfullscreen async autofocus autoplay checked controls default defer disabled
		formnovalidate inert ismap itemscope loop multiple muted nomodule novalidate open playsinline readonly required
		reversed selected shadowrootclonable shadowrootdelegatesfocus shadowrootserializable disablepictureinpicture
		disableremoteplayback alpha`) {
		attrInfos[attrID(a)].boolean = true
	}
	for a := range strings.FieldsSeq("popovertarget commandfor list form aria-controls aria-describedby aria-labelledby") {
		attrInfos[attrID(a)].idref = true
	}
	for a, vals := range enumAttrs {
		attrInfos[attrID(a)].enum = vals
	}
	for _, attrs := range elemEnumAttrs {
		for a := range attrs {
			attrInfos[attrID(a)].perElement = true
		}
	}
	for a := range strings.FieldsSeq(`viewbox fill stroke stroke-width stroke-linecap stroke-linejoin stroke-miterlimit
		stroke-dasharray fill-rule clip-rule d cx cy r rx ry x y x1 y1 x2 y2 points transform focusable aria-hidden
		aria-label aria-labelledby aria-describedby aria-controls aria-expanded aria-current offset stop-color
		gradientunits gradienttransform opacity fill-opacity stroke-opacity font-size font-family font-weight text-anchor
		dominant-baseline preserveaspectratio xmlns:xlink xlink:href clip-path mask filter`) {
		attrID(a)
	}
}

func knownAttr(e, a int) bool {
	return a >= 0 && (attrInfos[a].global || elems[e].attrs.has(a))
}
