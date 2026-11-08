package build

import (
	"strconv"
	"strings"

	"github.com/zeebo/xxh3"
)

const (
	cardW        = 1200
	cardH        = 630
	cardMarginX  = 80
	cardMarginY  = 56
	cardTitlePx  = 52
	cardTitleLH  = 65
	cardDescPx   = 30
	cardDescLH   = 43
	cardGap      = 28
	cardMaxTitle = 3
	cardBG       = "#f3f4f6"
	cardFG       = "#0b1220"
	cardFont     = "-apple-system,system-ui,Segoe UI,Roboto,Helvetica,Arial,sans-serif"
	cardSlack    = 1.08
)

var cardRegular = [95]uint16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

var cardBold = [95]uint16{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
}

var cardFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ç': 'c', 'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ñ': 'n', 'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ý': 'y', 'ÿ': 'y', 'À': 'A', 'Á': 'A', 'Â': 'A', 'Ã': 'A', 'Ä': 'A',
	'Å': 'A', 'Ç': 'C', 'È': 'E', 'É': 'E', 'Ê': 'E', 'Ë': 'E', 'Ì': 'I', 'Í': 'I', 'Î': 'I', 'Ï': 'I', 'Ñ': 'N',
	'Ò': 'O', 'Ó': 'O', 'Ô': 'O', 'Õ': 'O', 'Ö': 'O', 'Ø': 'O', 'Ù': 'U', 'Ú': 'U', 'Û': 'U', 'Ü': 'U', 'Ý': 'Y',
	'’': '\'', '‘': '\'', '“': '"', '”': '"', '„': '"', '«': '"', '»': '"', '‹': '\'', '›': '\'', '−': '-', ' ': ' ',
}

var cardWide = map[rune]uint16{'æ': 889, 'Æ': 1000, 'ß': 611, '–': 556, '—': 1000, '…': 1000, '€': 556, '§': 556, '°': 400, '·': 278}

func cardAdvance(r rune, bold bool) float64 {
	if f, ok := cardFold[r]; ok {
		r = f
	}
	if r >= 32 && r < 127 {
		if bold {
			return float64(cardBold[r-32])
		}
		return float64(cardRegular[r-32])
	}
	if w, ok := cardWide[r]; ok {
		return float64(w)
	}
	return 600
}

func cardWidth(s string, px int, bold bool) float64 {
	var units float64
	for _, r := range s {
		units += cardAdvance(r, bold)
	}
	return units * float64(px) / 1000 * cardSlack
}

func cardWrap(text string, px int, bold bool, maxLines int) []string {
	words := strings.Fields(text)
	if len(words) == 0 || maxLines <= 0 {
		return nil
	}
	limit := float64(cardW - 2*cardMarginX)
	var lines []string
	cur := ""
	for i, w := range words {
		next := w
		if cur != "" {
			next = cur + " " + w
		}
		if cur == "" || cardWidth(next, px, bold) <= limit {
			cur = next
			continue
		}
		if len(lines) == maxLines-1 {
			return append(lines, cardEllipsis(cur+" "+strings.Join(words[i:], " "), px, bold, limit))
		}
		lines = append(lines, cur)
		cur = w
	}
	if cardWidth(cur, px, bold) > limit {
		cur = cardEllipsis(cur+" …", px, bold, limit)
	}
	return append(lines, cur)
}

func cardEllipsis(s string, px int, bold bool, limit float64) string {
	words := strings.Fields(s)
	for n := len(words) - 1; n > 0; n-- {
		cand := strings.TrimRight(strings.Join(words[:n], " "), ",;:–—-") + " …"
		if cardWidth(cand, px, bold) <= limit {
			return cand
		}
	}
	r := []rune(words[0])
	for n := len(r) - 1; n > 0; n-- {
		if cand := string(r[:n]) + "…"; cardWidth(cand, px, bold) <= limit {
			return cand
		}
	}
	return "…"
}

func cardLayout(title, desc string) (tl, dl []string) {
	tl = cardWrap(title, cardTitlePx, true, cardMaxTitle)
	room := cardH - 2*cardMarginY - len(tl)*cardTitleLH
	if len(tl) > 0 {
		room -= cardGap
	}
	return tl, cardWrap(desc, cardDescPx, false, room/cardDescLH)
}

func cardSVG(title, desc string) []byte {
	tl, dl := cardLayout(title, desc)
	height := len(tl)*cardTitleLH + len(dl)*cardDescLH
	if len(tl) > 0 && len(dl) > 0 {
		height += cardGap
	}
	first := cardTitlePx
	if len(tl) == 0 {
		first = cardDescPx
	}
	y := (cardH-height)/2 + first
	b := make([]byte, 0, 1024)
	b = append(b, `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630"><rect width="100%" height="100%" fill="`+cardBG+`"/><g fill="`+cardFG+`" font-family="`+cardFont+`" text-anchor="middle">`...)
	text := func(lines []string, px, lh int, bold bool) {
		if len(lines) == 0 {
			return
		}
		b = append(b, `<text y="`...)
		b = strconv.AppendInt(b, int64(y), 10)
		b = append(b, `" font-size="`...)
		b = strconv.AppendInt(b, int64(px), 10)
		if bold {
			b = append(b, `" font-weight="700`...)
		}
		b = append(b, `">`...)
		for i, l := range lines {
			b = append(b, `<tspan x="600" dy="`...)
			if i > 0 {
				b = strconv.AppendInt(b, int64(lh), 10)
			} else {
				b = append(b, '0')
			}
			b = append(b, `">`...)
			b = escapeText(b, l)
			b = append(b, `</tspan>`...)
		}
		b = append(b, `</text>`...)
		y += len(lines) * lh
	}
	text(tl, cardTitlePx, cardTitleLH, true)
	if len(tl) > 0 {
		y += cardGap
	}
	text(dl, cardDescPx, cardDescLH, false)
	return append(b, `</g></svg>`...)
}

func cardStamp(pg *Page) string {
	return "card:" + strconv.FormatUint(xxh3.HashString(pg.Title+"\x00"+pg.Description), 36)
}

func newCard(pg *Page) *Resource {
	res := &Resource{idx: -1, Name: "card.svg", RelPermalink: pg.RelPermalink + "card.svg", MediaType: "image/svg+xml", page: pg, card: true}
	res.sizeOnce.Do(func() { res.w, res.h = cardW, cardH })
	return res
}

func (p *Project) planCards(records map[string]*OutRecord, old, ns *State, files map[string]bool) []writeJob {
	byKey := make(map[string]*Page, len(p.pages))
	for _, pg := range p.pages {
		byKey[pageKey(pg)] = pg
	}
	seen := map[string]bool{}
	var todo []writeJob
	for _, out := range sortedKeys(records) {
		for _, k := range records[out].Cards {
			pg := byKey[k]
			if seen[k] || pg == nil {
				continue
			}
			seen[k] = true
			dst := strings.TrimPrefix(pg.card.RelPermalink, "/")
			if files[dst] {
				p.errs.add("%s: card.svg collides with a bundle or static file at /%s; rename that file", pg.File, dst)
				continue
			}
			stamp := cardStamp(pg)
			files[dst] = true
			ns.Files[dst] = stamp
			if old != nil && old.Files[dst] == stamp {
				continue
			}
			todo = append(todo, writeJob{dst: dst, res: pg.card})
		}
	}
	return todo
}
