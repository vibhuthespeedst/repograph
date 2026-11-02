// Package extract builds a graph from a Go module's source. Nothing in the graph is
// written by hand, so rebuilding it is all it takes to keep it current.
package extract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/vibhuthespeedst/repograph/internal/graph"
)

var createTableRE = regexp.MustCompile(`(?i)CREATE\s+(?:UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?(\w+)"?`)

type tableMatcher struct {
	name string
	re   *regexp.Regexp
}

// findTables reads table names from the repo's .sql files and builds a matcher for
// each. A string counts as using a table when it is exactly the name or names it
// right after a SQL keyword.
func findTables(root string) []tableMatcher {
	names := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if SkipDir(d.Name()) && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".sql") {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			for _, m := range createTableRE.FindAllStringSubmatch(string(b), -1) {
				names[m[1]] = true
			}
		}
		return nil
	})
	out := make([]tableMatcher, 0, len(names))
	for n := range names {
		q := regexp.QuoteMeta(n)
		out = append(out, tableMatcher{n, regexp.MustCompile(`^` + q + `$|(?i:\b(?:FROM|INTO|UPDATE|JOIN|TABLE))\s+"?` + q + `\b`)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// SkipDir reports directories that are never part of the build.
func SkipDir(name string) bool {
	return name == "vendor" || name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// builder collects the graph while packages are walked.
type builder struct {
	mod, rootName, root string
	tables              []tableMatcher
	funcs               map[string]graph.Func
	calls, used         map[string]map[string]bool
	imports             map[string]map[string]bool
	varUses             [][2]string // from -> package-level var, kept only if the var refers to something
	routes              []graph.Route
	ifaces, concretes   []*types.TypeName
}

func addEdge(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func flatten(m map[string]map[string]bool) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, vs := range m {
		list := make([]string, 0, len(vs))
		for v := range vs {
			list = append(list, v)
		}
		sort.Strings(list)
		out[k] = list
	}
	return out
}

// short turns full names into IDs: (*example.com/m/internal/store.DB).Get is
// (*store.DB).Get.
func (b *builder) short(s string) string {
	s = strings.ReplaceAll(s, b.mod+"/internal/", "")
	s = strings.ReplaceAll(s, b.mod+"/", "")
	if b.rootName != "" {
		s = strings.ReplaceAll(s, b.mod+".", b.rootName+".")
	}
	return s
}

func (b *builder) pkgLabel(path string) string {
	if path == b.mod {
		return b.rootName
	}
	return strings.TrimPrefix(path, b.mod+"/")
}

func (b *builder) inRepo(p *types.Package) bool {
	return p != nil && (p.Path() == b.mod || strings.HasPrefix(p.Path(), b.mod+"/"))
}

func (b *builder) funcID(f *types.Func) string { return b.short(f.Origin().FullName()) }

func (b *builder) varID(v *types.Var) string { return b.short(v.Pkg().Path() + "." + v.Name()) }

func (b *builder) rel(p string) string {
	r, err := filepath.Rel(b.root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(r)
}

func (b *builder) noteString(from, s string) {
	for _, t := range b.tables {
		if t.re.MatchString(s) {
			addEdge(b.used, from, t.name)
		}
	}
}

// scan records what a function body or a variable's value refers to. Any reference
// counts, not only a direct call, so a handler passed to a router is still an edge.
func (b *builder) scan(p *packages.Package, from string, isTest bool, node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			switch u := p.TypesInfo.Uses[x].(type) {
			case *types.Func:
				if !b.inRepo(u.Pkg()) {
					return true
				}
				callee := b.funcID(u)
				addEdge(b.calls, from, callee)
				if _, ok := b.funcs[callee]; !ok {
					pos := p.Fset.Position(u.Pos())
					b.funcs[callee] = graph.Func{ID: callee, Pkg: b.pkgLabel(u.Pkg().Path()), File: b.rel(pos.Filename), Line: pos.Line}
				}
			case *types.Var:
				if b.inRepo(u.Pkg()) && !u.IsField() && u.Pkg().Scope().Lookup(u.Name()) == u {
					b.varUses = append(b.varUses, [2]string{from, b.varID(u)})
				}
			case *types.Const:
				if !isTest && u.Val().Kind() == constant.String {
					b.noteString(from, constant.StringVal(u.Val()))
				}
			}
		case *ast.BasicLit:
			if !isTest && x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil {
					b.noteString(from, s)
				}
			}
		}
		return true
	})
}

// codeHash covers the tokens of the code, so comments and layout don't affect it.
func codeHash(fset *token.FileSet, nodes ...ast.Node) string {
	h := sha256.New()
	for _, n := range nodes {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, n); err != nil {
			continue
		}
		var s scanner.Scanner
		file := token.NewFileSet().AddFile("", -1, buf.Len())
		s.Init(file, buf.Bytes(), nil, 0)
		for {
			_, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.SEMICOLON {
				fmt.Fprintf(h, "%d %s\n", tok, lit)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func firstLine(c *ast.CommentGroup) string {
	if c == nil {
		return ""
	}
	return strings.SplitN(strings.TrimSpace(c.Text()), "\n", 2)[0]
}

func (b *builder) addFuncDecl(p *packages.Package, fd *ast.FuncDecl, file string, isTest bool) {
	if fd.Body == nil {
		return
	}
	obj, _ := p.TypesInfo.Defs[fd.Name].(*types.Func)
	if obj == nil {
		return
	}
	id := b.funcID(obj)
	b.funcs[id] = graph.Func{
		ID: id, Pkg: b.pkgLabel(p.PkgPath), File: file, Line: p.Fset.Position(fd.Pos()).Line,
		Doc: firstLine(fd.Doc), Test: isTest, Hash: codeHash(p.Fset, fd.Type, fd.Body),
	}
	b.scan(p, id, isTest, fd.Body)
	if !isTest && !strings.Contains(p.ID, "[") {
		b.collectRoutes(p, fd.Body, "")
	}
}

func (b *builder) addVarDecl(p *packages.Package, gd *ast.GenDecl, file string, isTest bool) {
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			obj, _ := p.TypesInfo.Defs[name].(*types.Var)
			if obj == nil || name.Name == "_" || len(vs.Values) == 0 {
				continue
			}
			id := b.varID(obj)
			values := vs.Values
			if len(vs.Values) == len(vs.Names) {
				values = vs.Values[i : i+1]
			}
			nodes := make([]ast.Node, len(values))
			for j, v := range values {
				nodes[j] = v
				b.scan(p, id, isTest, v)
			}
			doc := vs.Doc
			if doc == nil {
				doc = gd.Doc
			}
			b.funcs[id] = graph.Func{
				ID: id, Pkg: b.pkgLabel(p.PkgPath), File: file, Line: p.Fset.Position(name.Pos()).Line,
				Doc: firstLine(doc), Kind: graph.KindVar, Test: isTest, Hash: codeHash(p.Fset, nodes...),
			}
		}
	}
}

// resolveVars keeps a package-level variable only if its value leads somewhere,
// then links what uses it. Each pass can bring in a var whose value is another var.
func (b *builder) resolveVars() {
	kept := map[string]bool{}
	for id, f := range b.funcs {
		if f.Kind == graph.KindVar && (len(b.calls[id]) > 0 || len(b.used[id]) > 0) {
			kept[id] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, u := range b.varUses {
			if !kept[u[1]] {
				continue
			}
			if f := b.funcs[u[0]]; f.Kind == graph.KindVar && !kept[u[0]] {
				kept[u[0]] = true
				changed = true
			}
		}
	}
	for _, u := range b.varUses {
		if kept[u[1]] {
			addEdge(b.calls, u[0], u[1])
		}
	}
	for id, f := range b.funcs {
		if f.Kind == graph.KindVar && !kept[id] {
			delete(b.funcs, id)
		}
	}
}

// linkInterfaces leads an interface method to every repo type that implements it,
// so a call through handler.StoreAPI reaches (*store.Store).
func (b *builder) linkInterfaces() {
	for _, it := range b.ifaces {
		iface := it.Type().Underlying().(*types.Interface)
		for _, c := range b.concretes {
			ptr := types.NewPointer(c.Type())
			if !types.Implements(ptr, iface) {
				continue
			}
			for i := 0; i < iface.NumMethods(); i++ {
				m := iface.Method(i)
				impl, _, _ := types.LookupFieldOrMethod(ptr, true, c.Pkg(), m.Name())
				if fn, ok := impl.(*types.Func); ok && b.inRepo(fn.Pkg()) && b.inRepo(m.Pkg()) {
					addEdge(b.calls, b.funcID(m), b.funcID(fn))
				}
			}
		}
	}
}

func (b *builder) addTypes(p *packages.Package) {
	for _, name := range p.Types.Scope().Names() {
		tn, ok := p.Types.Scope().Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		if it, ok := tn.Type().Underlying().(*types.Interface); ok {
			if it.NumMethods() > 0 {
				b.ifaces = append(b.ifaces, tn)
			}
		} else {
			b.concretes = append(b.concretes, tn)
		}
	}
}

// Build reads the module at dir. Packages that fail to compile become warnings and
// the rest are still analysed, so a broken file doesn't cost you the whole graph.
func Build(dir string, tags []string) (*graph.Graph, []string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedModule,
		Dir:   root,
		Tests: true,
	}
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, nil, err
	}

	b := &builder{
		root: root, tables: findTables(root), funcs: map[string]graph.Func{},
		calls: map[string]map[string]bool{}, used: map[string]map[string]bool{}, imports: map[string]map[string]bool{},
	}
	for _, p := range pkgs {
		if p.Module != nil && b.mod == "" {
			b.mod = p.Module.Path
		}
	}
	if b.mod == "" {
		return nil, nil, errors.New("no Go module found; run this inside a module, or pass -C <dir>")
	}
	for _, p := range pkgs {
		if p.PkgPath == b.mod {
			b.rootName = p.Name
			break
		}
	}

	seenWarning := map[string]bool{}
	var warnings []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			if m := e.Error(); !seenWarning[m] {
				seenWarning[m] = true
				warnings = append(warnings, m)
			}
		}
		if strings.HasSuffix(p.ID, ".test") || p.TypesInfo == nil || p.Types == nil {
			continue
		}
		if !strings.Contains(p.ID, "[") {
			for path := range p.Imports {
				if path == b.mod || strings.HasPrefix(path, b.mod+"/") {
					addEdge(b.imports, b.pkgLabel(p.PkgPath), b.pkgLabel(path))
				}
			}
			b.addTypes(p)
		}
		for _, f := range p.Syntax {
			file := b.rel(p.Fset.Position(f.Pos()).Filename)
			isTest := strings.HasSuffix(file, "_test.go")
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					b.addFuncDecl(p, d, file, isTest)
				case *ast.GenDecl:
					if d.Tok == token.VAR {
						b.addVarDecl(p, d, file, isTest)
					}
				}
			}
		}
	}
	if len(b.funcs) == 0 && len(warnings) > 0 {
		return nil, warnings, fmt.Errorf("nothing could be analysed: %s", warnings[0])
	}
	b.resolveVars()
	b.linkInterfaces()

	g := &graph.Graph{Version: graph.FormatVersion, Module: b.mod, Imports: flatten(b.imports), Calls: flatten(b.calls), Tables: flatten(b.used)}
	for _, f := range b.funcs {
		g.Funcs = append(g.Funcs, f)
	}
	sort.Slice(g.Funcs, func(i, j int) bool { return g.Funcs[i].ID < g.Funcs[j].ID })
	seenRoute := map[string]bool{}
	for _, r := range b.routes {
		k := r.Method + " " + r.Path
		if !seenRoute[k] {
			seenRoute[k] = true
			g.Routes = append(g.Routes, r)
		}
	}
	sort.Slice(g.Routes, func(i, j int) bool {
		if g.Routes[i].Path != g.Routes[j].Path {
			return g.Routes[i].Path < g.Routes[j].Path
		}
		return g.Routes[i].Method < g.Routes[j].Method
	})
	return g, warnings, nil
}

// collectRoutes reads go-chi registrations, joining nested r.Route prefixes.
func (b *builder) collectRoutes(p *packages.Package, body ast.Node, prefix string) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		t := p.TypesInfo.TypeOf(sel.X)
		if t == nil || !strings.Contains(t.String(), "go-chi/chi") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		path, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		switch sel.Sel.Name {
		case "Route":
			if fl, ok := call.Args[1].(*ast.FuncLit); ok {
				b.collectRoutes(p, fl.Body, prefix+path)
			}
			return false
		case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options":
			full := prefix + path
			if len(full) > 1 {
				full = strings.TrimSuffix(full, "/")
			}
			pos := p.Fset.Position(call.Pos())
			b.routes = append(b.routes, graph.Route{
				Method: strings.ToUpper(sel.Sel.Name), Path: full, Handler: b.handlerID(p, call.Args[1]),
				File: b.rel(pos.Filename), Line: pos.Line,
			})
			return false
		}
		return true
	})
}

func (b *builder) handlerID(p *packages.Package, arg ast.Expr) string {
	var ident *ast.Ident
	switch h := arg.(type) {
	case *ast.SelectorExpr:
		ident = h.Sel
	case *ast.Ident:
		ident = h
	}
	if ident != nil {
		if fn, ok := p.TypesInfo.Uses[ident].(*types.Func); ok && b.inRepo(fn.Pkg()) {
			return b.funcID(fn)
		}
	}
	return "(inline)"
}
