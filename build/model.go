package build

import (
	"sync"
	"time"

	"github.com/beint-no/lyn/yamlite"
)

type HTML string

type Attr string

type JSON string

type Pages []*Page

type Language struct {
	Code   string
	Locale string
	Label  string
	Title  string
	Weight int
	Prefix string
	site   *Site
}

type Page struct {
	idx           int32
	key           string
	Kind          string
	Lang          *Language
	Path          string
	Dir           string
	File          string
	RelPermalink  string
	Title         string
	SeoTitle      string
	Description   string
	Summary       string
	Layout        string
	Date          time.Time
	Lastmod       time.Time
	Weight        int
	NoIndex       bool
	Params        *yamlite.Map
	toolParams    *yamlite.Map
	translationOf string
	group         string
	ownLastmod    bool
	Parent        *Page
	Children      Pages
	Translations  Pages
	Resources     []*Resource
	card          *Resource
	section       int32
	site          *Site
	body          []byte
	bodyLine      int
	metaHash      uint64
	bodyHash      uint64

	contentOnce sync.Once
	content     HTML
	toc         HTML
	ids         []string
	wordsOnce   sync.Once
	words       int
	contentErr  error
	contentRec  *recorder
	summaryOnce sync.Once
	summaryHTML HTML
	summaryErr  error
	summaryRec  *recorder
	sortHash    uint64
	titleKey    []byte
	rprOnce     sync.Once
	rpr         Pages
}

func (p *Page) IsHome() bool    { return p.Kind == "home" }
func (p *Page) IsSection() bool { return p.Kind == "section" || p.Kind == "home" }
func (p *Page) IsPage() bool    { return p.Kind == "page" }

type Resource struct {
	idx          int32
	Name         string
	RelPermalink string
	Integrity    string
	MediaType    string
	src          string
	out          string
	data         []byte
	page         *Page
	asset        bool
	card         bool
	hash         uint64
	mu           sync.Mutex
	outName      string
	processed    bool
	procErr      error
	procOnce     sync.Once
	stamp        string
	sizeOnce     sync.Once
	w, h         int
}

type MenuEntry struct {
	Name     string
	URL      string
	Page     *Page
	Weight   int
	Params   *yamlite.Map
	Children []any
	Title    string
	parent   string
}

type Site struct {
	Title    string
	BaseURL  string
	Language *Language
	Params   *yamlite.Map
	Menus    *yamlite.Map
	Home     *Page
	Pages    Pages
	project  *Project
	byPath   map[string]*Page
	sections Pages
	i18n     map[string]string
	notFound *Page
	regOnce  sync.Once
	regular  Pages
}

type Section struct {
	page *Page
}
