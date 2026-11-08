package build

import (
	"strconv"
	"time"
)

func Inspect(root string) ([]byte, error) {
	p, err := Load(root)
	if err != nil {
		return nil, err
	}
	b := []byte(`{"baseURL":`)
	b = appendJSONString(b, p.BaseURL+"/")
	b = append(b, `,"title":`...)
	b = appendJSONString(b, p.Title)
	b = append(b, `,"params":`...)
	b = appendJSON(b, p.params, "site.yml")
	b = append(b, `,"languages":[`...)
	for i, l := range p.Languages {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"code":`...)
		b = appendJSONString(b, l.Code)
		b = append(b, `,"locale":`...)
		b = appendJSONString(b, l.Locale)
		b = append(b, `,"prefix":`...)
		b = appendJSONString(b, l.Prefix)
		b = append(b, '}')
	}
	b = append(b, `],"pages":[`...)
	for i, pg := range p.pages {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"lang":`...)
		b = appendJSONString(b, pg.Lang.Code)
		dates := [2]string{}
		for i, t := range []time.Time{pg.Date, pg.Lastmod} {
			if !t.IsZero() {
				dates[i] = t.Format(time.RFC3339)
			}
		}
		b = append(b, `,"weight":`...)
		b = strconv.AppendInt(b, int64(pg.Weight), 10)
		for _, f := range [][2]string{{"kind", pg.Kind}, {"path", pg.Path}, {"url", pg.RelPermalink}, {"file", pg.File}, {"layout", pg.Layout}, {"title", pg.Title}, {"seoTitle", pg.SeoTitle}, {"description", pg.Description}, {"summary", pg.Summary}, {"date", dates[0]}, {"lastmod", dates[1]}, {"translationOf", pg.translationOf}} {
			b = append(b, `,"`+f[0]+`":`...)
			b = appendJSONString(b, f[1])
		}
		b = append(b, `,"noindex":`...)
		if pg.NoIndex {
			b = append(b, "true"...)
		} else {
			b = append(b, "false"...)
		}
		b = append(b, `,"params":`...)
		b = appendJSON(b, pg.Params, pg.File)
		b = append(b, `,"toolParams":`...)
		b = appendJSON(b, pg.toolParams, pg.File)
		b = append(b, '}')
	}
	return append(b, "]}\n"...), nil
}
