package build

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
		for _, f := range [][2]string{{"kind", pg.Kind}, {"path", pg.Path}, {"url", pg.RelPermalink}, {"file", pg.File}, {"layout", pg.Layout}, {"title", pg.Title}, {"seoTitle", pg.SeoTitle}, {"description", pg.Description}, {"summary", pg.Summary}} {
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
		b = append(b, '}')
	}
	return append(b, "]}\n"...), nil
}
