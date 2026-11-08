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
	cardSlack    = 1.03
)

func cardWidth(s string, px int, bold bool) float64 {
	fc := fontFace(bold)
	var em float64
	for _, r := range s {
		em += fc.advance(r)
	}
	return em * float64(px) * cardSlack
}

func cardWrap(text string, px int, bold bool, maxLines int) []string {
	limit := float64(cardW - 2*cardMarginX)
	return wrapLines(text, maxLines, func(s string) bool { return cardWidth(s, px, bold) <= limit })
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
