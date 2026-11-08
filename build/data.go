package build

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/beint-no/lyn/yamlite"
)

func (p *Project) loadData() error {
	p.data = yamlite.NewMap()
	dir := filepath.Join(p.Root, "data")
	type dataFile struct {
		rel, full string
		v         any
		err       error
	}
	var found []*dataFile
	walkErr := filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, full)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && !segmentRE.MatchString(strings.ReplaceAll(d.Name(), "_", "-")) {
				return fmt.Errorf("data/%s: folder names must be lowercase ASCII", rel)
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".json") {
			return fmt.Errorf("data/%s: data files are JSON (.json)", rel)
		}
		found = append(found, &dataFile{rel: rel, full: full})
		return nil
	})
	var wg sync.WaitGroup
	for _, f := range found {
		wg.Go(func() {
			src, err := os.ReadFile(f.full)
			if err != nil {
				f.err = err
				return
			}
			if f.v, err = parseJSON(src); err != nil {
				f.err = fmt.Errorf("data/%s: %v", f.rel, err)
			}
		})
	}
	wg.Wait()
	for _, f := range found {
		if f.err != nil {
			return f.err
		}
		rel, v := f.rel, f.v
		parent := p.data
		parts := strings.Split(strings.TrimSuffix(rel, ".json"), "/")
		for _, seg := range parts[:len(parts)-1] {
			next, ok := parent.Peek(seg)
			if !ok {
				m := yamlite.NewMap()
				parent.Set(seg, m, 0)
				next = m
			}
			parent = next.(*yamlite.Map)
		}
		key := parts[len(parts)-1]
		if !parent.Set(key, v, 0) {
			return fmt.Errorf("data/%s: name collides with a folder", rel)
		}
		p.trackMap(v, "data."+strings.ReplaceAll(strings.TrimSuffix(rel, ".json"), "/", "."))
		p.dataFiles = append(p.dataFiles, "data."+strings.ReplaceAll(strings.TrimSuffix(rel, ".json"), "/", "."))
	}
	return walkErr
}

func parseJSON(src []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	v, err := jsonValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the top-level value")
	}
	return v, nil
}

func jsonValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("offset %d: %v", dec.InputOffset(), err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := yamlite.NewMap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("offset %d: %v", dec.InputOffset(), err)
				}
				k := kt.(string)
				v, err := jsonValue(dec)
				if err != nil {
					return nil, err
				}
				if !m.Set(k, v, 0) {
					return nil, fmt.Errorf("offset %d: duplicate key %q", dec.InputOffset(), k)
				}
			}
			dec.Token()
			return m, nil
		case '[':
			out := []any{}
			for dec.More() {
				v, err := jsonValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			dec.Token()
			return out, nil
		}
	case string:
		return t, nil
	case bool:
		return t, nil
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), nil
		}
		f, err := t.Float64()
		return f, err
	case nil:
		return nil, fmt.Errorf("offset %d: null is not allowed; omit the key", dec.InputOffset())
	}
	return nil, fmt.Errorf("offset %d: unexpected token", dec.InputOffset())
}

func (p *Project) loadAssets() error {
	p.assets = map[string]*Resource{}
	dir := filepath.Join(p.Root, "assets")
	var stamps sync.WaitGroup
	defer stamps.Wait()
	return filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(dir, full)
		rel = filepath.ToSlash(rel)
		for _, seg := range strings.Split(rel, "/") {
			if !resourceRE.MatchString(seg) {
				p.errs.add("assets/%s: file names must be ASCII without spaces", rel)
			}
		}
		res := &Resource{Name: rel, src: path.Join("assets", rel), asset: true, MediaType: mediaType(rel)}
		p.assets[rel] = res
		stamps.Go(func() {
			if fi, err := d.Info(); err == nil {
				res.stamp = fileStamp(fi)
			}
		})
		return nil
	})
}
