package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/template/parse"

	"github.com/beint-no/lyn/yamlite"
	"github.com/zeebo/xxh3"
)

type stmt func(s *st)
type expr func(s *st) any

type tmpl struct {
	name     string
	file     string
	id       int
	root     stmt
	nvars    int
	returns  bool
	usesPage bool
	calls    []*tmpl
	files    []int
	compiled bool
	busy     bool
	trees    map[string]*parse.Tree
	memoOff  atomic.Bool
	calls_   atomic.Int64
	hits     atomic.Int64
	used     atomic.Bool
}

type templates struct {
	p          *Project
	files      []string
	fileHash   []uint64
	fileIDs    map[string]int
	base       map[string]*parse.Tree
	layouts    map[string]*tmpl
	partials   map[string]*tmpl
	shortcodes map[string]*tmpl
	srcs       map[string]map[string]*parse.Tree
	memo       memoCache
}

type st struct {
	r    *rctx
	dot  any
	vars []any
	out  []byte
	ctrl uint8
	ret  any
}

const (
	ctrlBreak    = 1
	ctrlContinue = 2
	ctrlReturn   = 3
)

type tplError struct {
	loc string
	msg string
}

func (e *tplError) Error() string { return e.loc + ": " + e.msg }

func fail(loc string, format string, args ...any) {
	panic(&tplError{loc, fmt.Sprintf(format, args...)})
}

func (p *Project) loadTemplates() error {
	t := &templates{p: p, fileIDs: map[string]int{}, layouts: map[string]*tmpl{}, partials: map[string]*tmpl{}, shortcodes: map[string]*tmpl{}, srcs: map[string]map[string]*parse.Tree{}}
	p.tpl = t
	dir := filepath.Join(p.Root, "layouts")
	err := filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(p.Root, full)
		rel = filepath.ToSlash(rel)
		inner := strings.TrimPrefix(rel, "layouts/")
		dirName, base := filepath.Split(inner)
		if !strings.HasSuffix(base, ".html") {
			p.errs.add("%s: templates must be .html files", rel)
			return nil
		}
		switch dirName {
		case "", "partials/", "shortcodes/":
		default:
			p.errs.add("%s: templates live in layouts/, layouts/partials/ or layouts/shortcodes/", rel)
			return nil
		}
		src, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		id := len(t.files)
		t.files = append(t.files, rel)
		t.fileHash = append(t.fileHash, xxh3.Hash(src))
		t.fileIDs[rel] = id
		tree := parse.New(rel)
		tree.Mode = parse.SkipFuncCheck | parse.ParseComments
		set := map[string]*parse.Tree{}
		if _, err := tree.Parse(string(src), "", "", set); err != nil {
			p.errs.add("%v", err)
			return nil
		}
		t.srcs[rel] = set
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("layouts/ is missing")
	}
	if err != nil {
		return err
	}
	if err := p.errs.err(); err != nil {
		return err
	}
	t.base = t.srcs["layouts/base.html"]
	for rel := range t.srcs {
		name := strings.TrimSuffix(filepath.Base(rel), ".html")
		switch {
		case strings.HasPrefix(rel, "layouts/partials/"):
			t.partials[name] = &tmpl{name: name, file: rel, id: t.fileIDs[rel], trees: t.srcs[rel]}
		case strings.HasPrefix(rel, "layouts/shortcodes/"):
			t.shortcodes[name] = &tmpl{name: name, file: rel, id: t.fileIDs[rel], trees: t.srcs[rel]}
		case rel == "layouts/base.html":
		default:
			t.layouts[name] = &tmpl{name: name, file: rel, id: t.fileIDs[rel], trees: t.srcs[rel]}
		}
	}
	names := make([]string, 0, len(t.layouts))
	for n := range t.layouts {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		t.compileLayout(t.layouts[n])
	}
	for _, sc := range t.shortcodes {
		t.compileUnit(sc, sc.trees[sc.file], sc.trees)
	}
	for _, pt := range t.partials {
		t.ensure(pt)
	}
	return p.errs.err()
}

func (t *templates) ensure(pt *tmpl) {
	if pt.compiled || pt.busy {
		return
	}
	pt.busy = true
	t.compileUnit(pt, pt.trees[pt.file], pt.trees)
	pt.busy = false
}

func onlyDefines(tr *parse.Tree) bool {
	for _, n := range tr.Root.Nodes {
		switch x := n.(type) {
		case *parse.TextNode:
			if strings.TrimSpace(string(x.Text)) != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (t *templates) compileLayout(l *tmpl) {
	own := l.trees[l.file]
	if onlyDefines(own) {
		if t.base == nil {
			t.p.errs.add("%s: defines blocks but layouts/base.html does not exist", l.file)
			return
		}
		set := map[string]*parse.Tree{}
		for k, v := range t.base {
			set[k] = v
		}
		for k, v := range l.trees {
			if k == l.file {
				continue
			}
			if _, ok := t.base[k]; !ok {
				t.p.errs.add("%s: defines %q but layouts/base.html has no block with that name", l.file, k)
			}
			set[k] = v
		}
		l.files = append(l.files, t.fileIDs["layouts/base.html"])
		t.compileUnit(l, set["layouts/base.html"], set)
		return
	}
	t.compileUnit(l, own, l.trees)
}

type scope struct {
	vars map[string]int
}

type compiler struct {
	t      *templates
	unit   *tmpl
	set    map[string]*parse.Tree
	tree   *parse.Tree
	scopes []scope
	nvars  int
	ctx    hctx
	defs   map[string]stmt
	busy   map[string]bool
	first  bool
}

func (t *templates) compileUnit(u *tmpl, tree *parse.Tree, set map[string]*parse.Tree) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*tplError); ok {
				t.p.errs.add("%s", e.Error())
				return
			}
			panic(r)
		}
	}()
	c := &compiler{t: t, unit: u, set: set, tree: tree, defs: map[string]stmt{}, busy: map[string]bool{}, first: true}
	c.push()
	c.declare("$")
	u.files = append(u.files, u.id)
	u.root = c.list(tree.Root)
	if c.ctx.state != sText {
		fail(u.file, "template ends inside an unfinished HTML tag or element")
	}
	u.nvars = c.nvars
	u.compiled = true
	slices.Sort(u.files)
	u.files = slices.Compact(u.files)
}

func (c *compiler) loc(n parse.Node) string {
	if n == nil {
		return c.tree.ParseName
	}
	l, _ := c.tree.ErrorContext(n)
	return l
}

func (c *compiler) push() { c.scopes = append(c.scopes, scope{vars: map[string]int{}}) }
func (c *compiler) pop()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *compiler) declare(name string) int {
	slot := c.nvars
	c.nvars++
	c.scopes[len(c.scopes)-1].vars[name] = slot
	return slot
}

func (c *compiler) lookup(n parse.Node, name string) int {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if slot, ok := c.scopes[i].vars[name]; ok {
			return slot
		}
	}
	fail(c.loc(n), "undefined variable %s", name)
	return 0
}

func seq(list []stmt) stmt {
	switch len(list) {
	case 0:
		return func(*st) {}
	case 1:
		return list[0]
	}
	return func(s *st) {
		for _, f := range list {
			f(s)
			if s.ctrl != 0 {
				return
			}
		}
	}
}

func (c *compiler) list(l *parse.ListNode) stmt {
	if l == nil {
		return func(*st) {}
	}
	var out []stmt
	var pending []byte
	flush := func() {
		if len(pending) > 0 {
			b := slices.Clone(pending)
			out = append(out, func(s *st) { s.out = append(s.out, b...) })
			pending = pending[:0]
		}
	}
	for _, n := range l.Nodes {
		if tn, ok := n.(*parse.TextNode); ok {
			text, ctx, err := processText(tn.Text, c.ctx, c.first)
			if err != nil {
				fail(c.loc(n), "%v", err)
			}
			c.ctx = ctx
			pending = append(pending, text...)
			continue
		}
		c.first = false
		c.ctx.known = false
		f := c.node(n)
		if f == nil {
			continue
		}
		flush()
		out = append(out, f)
	}
	flush()
	return seq(out)
}

func (c *compiler) node(n parse.Node) stmt {
	switch x := n.(type) {
	case *parse.ActionNode:
		return c.action(x)
	case *parse.IfNode:
		return c.branch(x.Pipe, x.List, x.ElseList, false, n)
	case *parse.WithNode:
		return c.branch(x.Pipe, x.List, x.ElseList, true, n)
	case *parse.RangeNode:
		return c.rangeNode(x)
	case *parse.TemplateNode:
		return c.templateCall(x)
	case *parse.BreakNode:
		return func(s *st) { s.ctrl = ctrlBreak }
	case *parse.ContinueNode:
		return func(s *st) { s.ctrl = ctrlContinue }
	case *parse.CommentNode:
		fail(c.loc(n), "template comments are not allowed")
	case *parse.ListNode:
		return c.list(x)
	}
	fail(c.loc(n), "unsupported template construct %s", n)
	return nil
}

func (c *compiler) action(a *parse.ActionNode) stmt {
	pipe := a.Pipe
	if len(pipe.Decl) > 0 {
		val := c.pipeline(pipe, nil)
		if pipe.IsAssign {
			slots := make([]int, len(pipe.Decl))
			for i, d := range pipe.Decl {
				slots[i] = c.lookup(d, d.Ident[0])
			}
			slot := slots[0]
			return func(s *st) { s.vars[slot] = val(s) }
		}
		if len(pipe.Decl) != 1 {
			fail(c.loc(a), "only one variable can be declared here")
		}
		slot := c.declare(pipe.Decl[0].Ident[0])
		return func(s *st) { s.vars[slot] = val(s) }
	}
	if ret := c.returnCall(pipe); ret != nil {
		return ret
	}
	mode, err := modeFor(c.ctx)
	if err != nil {
		fail(c.loc(a), "%v", err)
	}
	val := c.pipeline(pipe, nil)
	loc := c.loc(a)
	return func(s *st) { s.out = writeValue(s, s.out, val(s), mode, loc) }
}

func (c *compiler) returnCall(pipe *parse.PipeNode) stmt {
	if len(pipe.Cmds) == 0 {
		return nil
	}
	id, ok := pipe.Cmds[0].Args[0].(*parse.IdentifierNode)
	if !ok || id.Ident != "return" {
		return nil
	}
	if c.unit == nil || !strings.HasPrefix(c.unit.file, "layouts/partials/") {
		fail(c.loc(pipe), "return is only allowed in partials")
	}
	c.unit.returns = true
	args := pipe.Cmds[0].Args[1:]
	if len(args) == 0 {
		if len(pipe.Cmds) > 1 {
			fail(c.loc(pipe), "return needs a value before the pipe")
		}
		return func(s *st) { s.ret, s.ctrl = "", ctrlReturn }
	}
	inner := &parse.PipeNode{NodeType: parse.NodePipe, Pos: pipe.Pos, Line: pipe.Line}
	first := &parse.CommandNode{NodeType: parse.NodeCommand, Pos: pipe.Cmds[0].Pos, Args: args}
	inner.Cmds = append([]*parse.CommandNode{first}, pipe.Cmds[1:]...)
	v := c.pipeline(inner, nil)
	return func(s *st) { s.ret, s.ctrl = v(s), ctrlReturn }
}

func (c *compiler) branch(pipe *parse.PipeNode, list, elseList *parse.ListNode, with bool, n parse.Node) stmt {
	c.push()
	var cond expr
	var slot = -1
	if len(pipe.Decl) > 0 {
		if pipe.IsAssign || len(pipe.Decl) != 1 {
			fail(c.loc(n), "declare exactly one new variable in if/with")
		}
		cond = c.pipeline(pipe, nil)
		slot = c.declare(pipe.Decl[0].Ident[0])
	} else {
		cond = c.pipeline(pipe, nil)
	}
	start := c.ctx
	body := c.list(list)
	endBody := c.ctx
	c.pop()
	c.ctx = start
	var elseBody stmt
	if elseList != nil {
		c.push()
		elseBody = c.list(elseList)
		c.pop()
	}
	if !c.ctx.same(endBody) {
		fail(c.loc(n), "branches end in different HTML contexts (%v vs %v); close tags and quotes inside each branch", describe(endBody), describe(c.ctx))
	}
	if c.ctx.inTag() {
		c.ctx.state, c.ctx.attr = sTag, ""
	}
	c.ctx.known = false
	if with {
		return func(s *st) {
			v := cond(s)
			if slot >= 0 {
				s.vars[slot] = v
			}
			if truth(v) {
				old := s.dot
				s.dot = v
				body(s)
				s.dot = old
			} else if elseBody != nil {
				elseBody(s)
			}
		}
	}
	return func(s *st) {
		v := cond(s)
		if slot >= 0 {
			s.vars[slot] = v
		}
		if truth(v) {
			body(s)
		} else if elseBody != nil {
			elseBody(s)
		}
	}
}

func describe(c hctx) string {
	names := []string{"text", "tag", "attribute name", "attribute", "attribute value", "quoted attribute", "unquoted attribute", "script", "style", "pre", "raw text", "comment"}
	return names[c.state]
}

func (c *compiler) rangeNode(r *parse.RangeNode) stmt {
	c.push()
	coll := c.pipeline(r.Pipe, nil)
	kSlot, vSlot := -1, -1
	switch len(r.Pipe.Decl) {
	case 1:
		vSlot = c.declare(r.Pipe.Decl[0].Ident[0])
	case 2:
		kSlot = c.declare(r.Pipe.Decl[0].Ident[0])
		vSlot = c.declare(r.Pipe.Decl[1].Ident[0])
	}
	start := c.ctx
	body := c.list(r.List)
	if !c.ctx.same(start) {
		fail(c.loc(r), "range body must end in the HTML context it started in")
	}
	c.pop()
	var elseBody stmt
	if r.ElseList != nil {
		c.push()
		elseBody = c.list(r.ElseList)
		c.pop()
		if !c.ctx.same(start) {
			fail(c.loc(r), "range else must end in the HTML context it started in")
		}
	}
	c.ctx.known = false
	loc := c.loc(r)
	return func(s *st) {
		v := coll(s)
		old := s.dot
		n := 0
		each := func(k, e any) bool {
			n++
			if kSlot >= 0 {
				s.vars[kSlot] = k
			}
			if vSlot >= 0 {
				s.vars[vSlot] = e
			}
			s.dot = e
			body(s)
			switch s.ctrl {
			case ctrlBreak:
				s.ctrl = 0
				return false
			case ctrlContinue:
				s.ctrl = 0
			case ctrlReturn:
				return false
			}
			return true
		}
		iterate(s, v, loc, each)
		s.dot = old
		if n == 0 && elseBody != nil {
			elseBody(s)
		}
	}
}

func (c *compiler) templateCall(n *parse.TemplateNode) stmt {
	tr, ok := c.set[n.Name]
	if !ok {
		fail(c.loc(n), "no block or define named %q", n.Name)
	}
	if c.busy[n.Name] {
		fail(c.loc(n), "template %q calls itself", n.Name)
	}
	var arg expr = func(s *st) any { return nil }
	if n.Pipe != nil {
		arg = c.pipeline(n.Pipe, nil)
	}
	body, ok := c.defs[n.Name]
	if !ok {
		c.busy[n.Name] = true
		savedScopes, savedTree := c.scopes, c.tree
		c.scopes = []scope{{vars: map[string]int{}}}
		rootSlot := c.declare("$")
		c.tree = tr
		inner := c.list(tr.Root)
		c.tree, c.scopes = savedTree, savedScopes
		c.busy[n.Name] = false
		if id, ok := c.t.fileIDs[tr.ParseName]; ok {
			c.unit.files = append(c.unit.files, id)
		}
		body = func(s *st) {
			old := s.vars[rootSlot]
			s.vars[rootSlot] = s.dot
			inner(s)
			s.vars[rootSlot] = old
		}
		c.defs[n.Name] = body
	}
	return func(s *st) {
		old := s.dot
		s.dot = arg(s)
		body(s)
		s.dot = old
		if s.ctrl == ctrlBreak || s.ctrl == ctrlContinue {
			s.ctrl = 0
		}
	}
}

func (c *compiler) pipeline(p *parse.PipeNode, _ any) expr {
	var cur expr
	for i, cmd := range p.Cmds {
		cur = c.command(cmd, cur, i > 0)
	}
	return cur
}

func (c *compiler) command(cmd *parse.CommandNode, piped expr, hasPiped bool) expr {
	first := cmd.Args[0]
	switch x := first.(type) {
	case *parse.IdentifierNode:
		return c.call(x, cmd.Args[1:], piped, hasPiped, cmd)
	case *parse.FieldNode:
		return c.fieldCall(cmd, func(s *st) any { return s.dot }, x.Ident, cmd.Args[1:], piped, hasPiped)
	case *parse.ChainNode:
		var recv expr
		if pn, ok := x.Node.(*parse.PipeNode); ok {
			recv = c.pipeline(pn, nil)
		} else {
			recv = c.arg(x.Node)
		}
		return c.fieldCall(cmd, recv, x.Field, cmd.Args[1:], piped, hasPiped)
	case *parse.VariableNode:
		slot := c.lookup(x, x.Ident[0])
		recv := func(s *st) any { return s.vars[slot] }
		if len(x.Ident) == 1 {
			c.noArgs(cmd, hasPiped)
			return recv
		}
		return c.fieldCall(cmd, recv, x.Ident[1:], cmd.Args[1:], piped, hasPiped)
	}
	c.noArgs(cmd, hasPiped)
	return c.arg(first)
}

func (c *compiler) noArgs(cmd *parse.CommandNode, hasPiped bool) {
	if len(cmd.Args) > 1 || hasPiped {
		fail(c.loc(cmd), "%s is not a function and cannot take arguments", cmd.Args[0])
	}
}

func (c *compiler) arg(n parse.Node) expr {
	switch x := n.(type) {
	case *parse.DotNode:
		return func(s *st) any { return s.dot }
	case *parse.StringNode:
		v := x.Text
		return func(*st) any { return v }
	case *parse.BoolNode:
		v := x.True
		return func(*st) any { return v }
	case *parse.NumberNode:
		switch {
		case x.IsInt:
			v := int(x.Int64)
			return func(*st) any { return v }
		case x.IsFloat:
			v := x.Float64
			return func(*st) any { return v }
		}
		fail(c.loc(n), "unsupported number %s", x.Text)
	case *parse.NilNode:
		fail(c.loc(n), "nil is not allowed")
	case *parse.PipeNode:
		return c.pipeline(x, nil)
	case *parse.FieldNode:
		return c.fieldCall(x, func(s *st) any { return s.dot }, x.Ident, nil, nil, false)
	case *parse.VariableNode:
		slot := c.lookup(x, x.Ident[0])
		recv := func(s *st) any { return s.vars[slot] }
		if len(x.Ident) == 1 {
			return recv
		}
		return c.fieldCall(x, recv, x.Ident[1:], nil, nil, false)
	case *parse.ChainNode:
		var recv expr
		if pn, ok := x.Node.(*parse.PipeNode); ok {
			recv = c.pipeline(pn, nil)
		} else {
			recv = c.arg(x.Node)
		}
		return c.fieldCall(x, recv, x.Field, nil, nil, false)
	case *parse.IdentifierNode:
		return c.call(x, nil, nil, false, n)
	}
	fail(c.loc(n), "unsupported argument %s", n)
	return nil
}

func (c *compiler) args(nodes []parse.Node, piped expr, hasPiped bool) []expr {
	out := make([]expr, 0, len(nodes)+1)
	for _, n := range nodes {
		out = append(out, c.arg(n))
	}
	if hasPiped {
		out = append(out, piped)
	}
	return out
}

func evalArgs(s *st, args []expr, buf []any) []any {
	for _, a := range args {
		buf = append(buf, a(s))
	}
	return buf
}

func (c *compiler) fieldCall(at parse.Node, recv expr, names []string, argNodes []parse.Node, piped expr, hasPiped bool) expr {
	loc := c.loc(at)
	accs := make([]*accessor, len(names))
	for i, n := range names {
		accs[i] = lookupAccessor(n)
		if accs[i] == nil {
			fail(loc, "unknown field .%s%s (fields start with an uppercase letter; map keys are lowerCamelCase)", n, suggest(n, keysOf(accessors)))
		}
	}
	last := accs[len(accs)-1]
	args := c.args(argNodes, piped, hasPiped)
	for _, a := range accs[:len(accs)-1] {
		if a.minArgs > 0 {
			fail(loc, ".%s needs arguments and cannot be chained", a.name)
		}
	}
	if len(args) < last.minArgs || len(args) > last.maxArgs {
		if last.maxArgs == 0 {
			fail(loc, ".%s takes no arguments", last.name)
		}
		fail(loc, ".%s takes %d argument(s)", last.name, last.minArgs)
	}
	if len(accs) == 1 && len(args) == 0 {
		a := last
		return func(s *st) any { return a.get(s, recv(s), nil, loc) }
	}
	return func(s *st) any {
		v := recv(s)
		for _, a := range accs[:len(accs)-1] {
			v = a.get(s, v, nil, loc)
		}
		if len(args) == 0 {
			return last.get(s, v, nil, loc)
		}
		var buf [4]any
		return last.get(s, v, evalArgs(s, args, buf[:0]), loc)
	}
}

func (c *compiler) call(id *parse.IdentifierNode, argNodes []parse.Node, piped expr, hasPiped bool, at parse.Node) expr {
	loc := c.loc(at)
	name := id.Ident
	switch name {
	case "and", "or":
		args := c.args(argNodes, piped, hasPiped)
		if len(args) < 2 {
			fail(loc, "%s needs at least two arguments", name)
		}
		isAnd := name == "and"
		return func(s *st) any {
			var v any
			for _, a := range args {
				v = a(s)
				if truth(v) != isAnd {
					return v
				}
			}
			return v
		}
	case "default":
		args := c.args(argNodes, piped, hasPiped)
		if len(args) != 2 {
			fail(loc, "default takes a fallback and a value")
		}
		return func(s *st) any {
			v := args[1](s)
			if truth(v) {
				return v
			}
			return args[0](s)
		}
	case "cond":
		args := c.args(argNodes, piped, hasPiped)
		if len(args) != 3 {
			fail(loc, "cond takes a condition and two values")
		}
		return func(s *st) any {
			if truth(args[0](s)) {
				return args[1](s)
			}
			return args[2](s)
		}
	case "partial":
		return c.partialCall(argNodes, piped, hasPiped, loc)
	case "page":
		if len(argNodes) > 0 || hasPiped {
			fail(loc, "page takes no arguments")
		}
		c.unit.usesPage = true
		return func(s *st) any { return s.r.page }
	case "site":
		if len(argNodes) > 0 || hasPiped {
			fail(loc, "site takes no arguments")
		}
		return func(s *st) any { return s.r.site }
	case "T":
		return c.i18nCall(argNodes, piped, hasPiped, loc)
	case "asset":
		if len(argNodes)+boolInt(hasPiped) != 1 {
			fail(loc, "asset takes one file name from assets/")
		}
		if sn, ok := argNodes[0].(*parse.StringNode); ok && !hasPiped {
			res := c.t.p.assets[sn.Text]
			if res == nil {
				fail(loc, "assets/%s does not exist%s", sn.Text, suggest(sn.Text, keysOf(c.t.p.assets)))
			}
			return func(s *st) any {
				s.r.useResource(res)
				return res
			}
		}
		name := c.args(argNodes, piped, hasPiped)[0]
		return func(s *st) any {
			n := toString(name(s), loc)
			res := s.r.p.assets[n]
			if res == nil {
				fail(loc, "assets/%s does not exist", n)
			}
			s.r.useResource(res)
			return res
		}
	case "return":
		fail(loc, "return must be the only command in its action")
	}
	f, ok := builtins[name]
	if !ok {
		fail(loc, "unknown function %q%s", name, suggest(name, append(keysOf(builtins), "and", "or", "default", "cond", "partial", "page", "site", "T", "asset", "return")))
	}
	args := c.args(argNodes, piped, hasPiped)
	if len(args) < f.min || (f.max >= 0 && len(args) > f.max) {
		fail(loc, "%s: wrong number of arguments (%d)", name, len(args))
	}
	fn := f.fn
	switch len(args) {
	case 1:
		a0 := args[0]
		return func(s *st) any {
			var buf [1]any
			buf[0] = a0(s)
			return fn(s, buf[:], loc)
		}
	case 2:
		a0, a1 := args[0], args[1]
		return func(s *st) any {
			var buf [2]any
			buf[0], buf[1] = a0(s), a1(s)
			return fn(s, buf[:], loc)
		}
	}
	return func(s *st) any {
		var buf [6]any
		return fn(s, evalArgs(s, args, buf[:0]), loc)
	}
}

func (c *compiler) i18nCall(argNodes []parse.Node, piped expr, hasPiped bool, loc string) expr {
	if len(argNodes) == 0 {
		fail(loc, "T needs a translation key")
	}
	sn, ok := argNodes[0].(*parse.StringNode)
	if !ok {
		fail(loc, "T needs a literal translation key so unused and missing keys can be found")
	}
	id, ok := c.t.p.i18nIDs[sn.Text]
	if !ok {
		fail(loc, "translation key %q does not exist in i18n/%s", sn.Text, suggest(sn.Text, c.t.p.i18nKeys))
	}
	key := sn.Text
	rest := c.args(argNodes[1:], piped, hasPiped)
	if len(rest) > 1 {
		fail(loc, "T takes a key and at most one format argument")
	}
	return func(s *st) any {
		s.r.rec.i18n = append(s.r.rec.i18n, int32(id))
		v := s.r.p.i18n[s.r.site.Language.Code][key]
		if len(rest) == 1 {
			a := rest[0](s)
			if !strings.Contains(v, "%") {
				fail(loc, "translation %q has no format verb for its argument", key)
			}
			return fmt.Sprintf(v, a)
		}
		return v
	}
}

func (c *compiler) partialCall(argNodes []parse.Node, piped expr, hasPiped bool, loc string) expr {
	if len(argNodes) == 0 {
		fail(loc, "partial needs a name")
	}
	sn, ok := argNodes[0].(*parse.StringNode)
	if !ok {
		fail(loc, "partial needs a literal name")
	}
	name := strings.TrimSuffix(sn.Text, ".html")
	pt := c.t.partials[name]
	if pt == nil {
		fail(loc, "layouts/partials/%s.html does not exist%s", name, suggest(name, keysOf(c.t.partials)))
	}
	if pt.busy {
		fail(loc, "partial %q calls itself", name)
	}
	c.t.ensure(pt)
	if !pt.compiled {
		fail(loc, "partial %q failed to compile", name)
	}
	if pt.usesPage {
		c.unit.usesPage = true
	}
	c.unit.files = append(c.unit.files, pt.files...)
	c.unit.calls = append(c.unit.calls, pt)
	rest := c.args(argNodes[1:], piped, hasPiped)
	if len(rest) > 1 {
		fail(loc, "partial takes a name and one context value; use dict for more")
	}
	var arg expr = func(*st) any { return nil }
	if len(rest) == 1 {
		arg = rest[0]
	}
	if c.ctx.state != sText && !pt.returns {
		fail(loc, "partial %q outputs markup and can only be used in HTML text, not inside a tag or attribute", name)
	}
	return func(s *st) any { return s.r.callPartial(pt, arg(s), loc) }
}

type rctx struct {
	p         *Project
	page      *Page
	site      *Site
	rec       *recorder
	rendering *Page
}

func (r *rctx) callPartial(pt *tmpl, dot any, loc string) any {
	pt.used.Store(true)
	m := &r.p.tpl.memo
	var key []byte
	memo := !pt.memoOff.Load()
	if memo {
		var kb [64]byte
		key = append(kb[:0], byte(pt.id), byte(pt.id>>8), byte(r.site.Language.Weight))
		if pt.usesPage {
			key = appendPageKey(key, r.page)
		}
		var ok bool
		key, ok = fingerprint(key, dot, 0)
		if ok {
			n := pt.calls_.Add(1)
			if e := m.get(key); e != nil {
				pt.hits.Add(1)
				r.rec.merge(e.rec)
				return e.val
			}
			if n == 256 && pt.hits.Load() < 16 {
				pt.memoOff.Store(true)
			}
		} else {
			memo = false
		}
	}
	var sub *recorder
	parent := r.rec
	if memo {
		sub = &recorder{}
		r.rec = sub
	}
	s := &st{r: r, dot: dot, vars: make([]any, pt.nvars), out: getBuf(0)}
	s.vars[0] = dot
	func() {
		defer func() {
			if memo {
				r.rec = parent
			}
		}()
		pt.root(s)
	}()
	var val any
	if s.ctrl == ctrlReturn {
		val = s.ret
	} else if pt.returns {
		fail(loc, "partial %q finished without return", pt.name)
	} else {
		val = HTML(s.out)
	}
	putBuf(s.out)
	if memo {
		sub.compact()
		parent.merge(sub)
		m.put(string(key), &memoEntry{val: val, rec: sub})
	}
	return val
}

func appendPageKey(b []byte, p *Page) []byte {
	switch {
	case p == nil:
		return append(b, 'N')
	case p.idx < 0:
		return append(b, 'X', byte(p.Lang.Weight))
	}
	return append(b, 'P', byte(p.idx), byte(p.idx>>8), byte(p.idx>>16), byte(p.idx>>24))
}

func fingerprint(b []byte, v any, depth int) ([]byte, bool) {
	if depth > 4 || len(b) > 512 {
		return b, false
	}
	switch t := v.(type) {
	case nil:
		return append(b, 'n'), true
	case string:
		b = append(b, 's')
		b = strconv.AppendInt(b, int64(len(t)), 10)
		b = append(b, ':')
		return append(b, t...), true
	case HTML:
		b = append(b, 'h')
		b = strconv.AppendInt(b, int64(len(t)), 10)
		b = append(b, ':')
		return append(b, t...), true
	case int:
		b = append(b, 'i')
		return strconv.AppendInt(b, int64(t), 10), true
	case bool:
		if t {
			return append(b, 'T'), true
		}
		return append(b, 'F'), true
	case *Page:
		return appendPageKey(append(b, 'p'), t), true
	case *Resource:
		b = append(b, 'r')
		return append(b, t.RelPermalink...), true
	case *yamlite.Map:
		if t.ID >= 0 {
			b = append(b, 'm')
			return strconv.AppendInt(b, int64(t.ID), 10), true
		}
		b = append(b, '{')
		var ok bool
		for i, k := range t.Keys {
			b = append(b, k...)
			b = append(b, '=')
			if b, ok = fingerprint(b, t.Vals[i], depth+1); !ok {
				return b, false
			}
		}
		return append(b, '}'), true
	case []any:
		if len(t) > 16 {
			return b, false
		}
		b = append(b, '[')
		var ok bool
		for _, e := range t {
			if b, ok = fingerprint(b, e, depth+1); !ok {
				return b, false
			}
		}
		return append(b, ']'), true
	}
	return b, false
}

type memoEntry struct {
	val any
	rec *recorder
}

type memoCache struct {
	shards [64]struct {
		mu sync.RWMutex
		m  map[string]*memoEntry
	}
}

func (m *memoCache) shard(key []byte) int { return int(xxh3.Hash(key) & 63) }

func (m *memoCache) get(key []byte) *memoEntry {
	sh := &m.shards[m.shard(key)]
	sh.mu.RLock()
	e := sh.m[string(key)]
	sh.mu.RUnlock()
	return e
}

func (m *memoCache) put(key string, e *memoEntry) {
	sh := &m.shards[m.shard([]byte(key))]
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = map[string]*memoEntry{}
	}
	sh.m[key] = e
	sh.mu.Unlock()
}

func (m *memoCache) reset() {
	for i := range m.shards {
		m.shards[i].mu.Lock()
		m.shards[i].m = nil
		m.shards[i].mu.Unlock()
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
