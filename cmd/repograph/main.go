// Command repograph answers questions about a Go repository from its source: what
// calls a function, which HTTP routes a change can affect, which tables a handler
// reaches. The graph is rebuilt automatically when the code changes.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vibhuthespeedst/repograph/internal/cache"
	"github.com/vibhuthespeedst/repograph/internal/docs"
	"github.com/vibhuthespeedst/repograph/internal/extract"
	"github.com/vibhuthespeedst/repograph/internal/gitref"
	"github.com/vibhuthespeedst/repograph/internal/graph"
)

const usage = `repograph answers questions about a Go repository from its source.
The graph rebuilds itself when the code changes.

Ask about the code:
  repograph impact <name>         what a change here can affect: routes, callers, tests
  repograph callers <name>        what uses this function
  repograph callees <name>        what this function uses
  repograph path <from> <to>      one shortest call chain between two functions
  repograph tables <name>         database tables this function reaches
  repograph routes [text]         HTTP routes and their handlers (go-chi)
  repograph find <text>           search function names and doc comments

Ask about a change:
  repograph affected <ref>        routes and tests your changes since <ref> can affect
  repograph diff <ref>            what changed in the code since <ref>
  repograph diff <a.json> <b.json>  what changed between two saved graphs

Keep docs and CI honest:
  repograph docs <file.md>...     fail if a doc names something that no longer exists
  repograph diagram               print a Mermaid diagram of the package imports
  repograph diagram -write <f.md> put the diagram between the markers in a file
  repograph diagram -check <f.md> fail if the diagram in a file is out of date
  repograph build -o <file.json>  save the graph to a file
  repograph check <file.json>     fail if a saved graph no longer matches the code

Flags:
  -C <dir>      run as if started in <dir>
  -tags <a,b>   build tags, such as -tags integration
  -json         print JSON instead of text

Names can be bare (Search) or full ((*store.Store).Search).
`

// errFailedCheck means a check ran and found problems. It exits 1, while mistakes in
// how the tool was used exit 2, so CI can tell them apart.
var errFailedCheck = errors.New("check failed")

type options struct {
	dir        string
	tags       []string
	asJSON     bool
	out        string
	write, chk string
}

func parseArgs(args []string) (options, []string, error) {
	o := options{dir: "."}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if name == "json" {
			o.asJSON = true
			continue
		}
		if name == "h" || name == "help" {
			return o, []string{"help"}, nil
		}
		var target *string
		var tagValue string
		switch name {
		case "C":
			target = &o.dir
		case "tags":
			target = &tagValue
		case "o":
			target = &o.out
		case "write":
			target = &o.write
		case "check":
			target = &o.chk
		default:
			return o, nil, fmt.Errorf("unknown flag %s", a)
		}
		if i+1 >= len(args) {
			return o, nil, fmt.Errorf("%s needs a value", a)
		}
		i++
		*target = args[i]
		if name == "tags" {
			for _, t := range strings.Split(tagValue, ",") {
				if t = strings.TrimSpace(t); t != "" {
					o.tags = append(o.tags, t)
				}
			}
			sort.Strings(o.tags)
		}
	}
	return o, rest, nil
}

// session loads the current graph once for a command.
type session struct {
	options
	root string
}

func (s *session) index() (*graph.Index, error) {
	r, err := cache.Load(s.root, s.tags)
	for _, w := range r.Warnings {
		fmt.Fprintln(os.Stderr, "repograph: warning:", w)
	}
	if err != nil {
		return nil, err
	}
	if r.Rebuilt {
		fmt.Fprintf(os.Stderr, "repograph: rebuilt the graph (%d functions)\n", len(r.Graph.Funcs))
	}
	return graph.NewIndex(r.Graph), nil
}

func (s *session) printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func printFuncs(ix *graph.Index, ids []string) {
	width := 0
	for _, id := range ids {
		width = max(width, len(id))
	}
	for _, id := range ids {
		f := ix.Func(id)
		if f.File == "" {
			fmt.Printf("  %s\n", id)
			continue
		}
		fmt.Printf("  %-*s  %s:%d\n", width, id, f.File, f.Line)
	}
}

func printRoutes(rs []graph.Route) {
	width := 0
	for _, r := range rs {
		width = max(width, len(r.Path))
	}
	for _, r := range rs {
		fmt.Printf("  %-7s %-*s  %s\n", r.Method, width, r.Path, r.Handler)
	}
}

func printImpact(ix *graph.Index, im graph.Impact) {
	fmt.Printf("Routes that can be affected (%d of %d):\n", len(im.Routes), im.TotalRoutes)
	if len(im.Routes) == 0 {
		fmt.Println("  none")
	}
	printRoutes(im.Routes)

	pkgs := map[string]int{}
	for _, id := range im.Reaching {
		pkgs[ix.Func(id).Pkg]++
	}
	names := make([]string, 0, len(pkgs))
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, p := range names {
		parts = append(parts, fmt.Sprintf("%s (%d)", p, pkgs[p]))
	}
	fmt.Printf("\nFunctions that can reach this: %d", len(im.Reaching))
	if len(parts) > 0 {
		fmt.Printf(", in %s", strings.Join(parts, ", "))
	}
	fmt.Println()

	if len(im.Direct) > 0 {
		fmt.Println("\nDirect callers:")
		printFuncs(ix, im.Direct)
	}
	fmt.Printf("\nTests that use this directly: %d\n", len(im.Tests))
	printFuncs(ix, im.Tests)
}

func printChanges(ix *graph.Index, c graph.Changes) {
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Printf("%s (%d):\n", title, len(items))
		for _, s := range items {
			fmt.Printf("  %s\n", s)
		}
		fmt.Println()
	}
	if len(c.FuncsChanged) > 0 && ix != nil {
		fmt.Printf("Changed functions (%d):\n", len(c.FuncsChanged))
		printFuncs(ix, c.FuncsChanged)
		fmt.Println()
	} else {
		section("Changed functions", c.FuncsChanged)
	}
	section("New functions", c.FuncsAdded)
	section("Removed functions", c.FuncsRemoved)
	section("New calls", c.CallsAdded)
	section("Removed calls", c.CallsRemoved)
	section("New routes", c.RoutesAdded)
	section("Removed routes", c.RoutesRemoved)
	section("New table use", c.TablesAdded)
	section("Removed table use", c.TablesRemoved)
	section("New imports", c.ImportsAdded)
	section("Removed imports", c.ImportsRemoved)
}

func need(rest []string, n int, form string) error {
	if len(rest) < n+1 {
		return fmt.Errorf("usage: repograph %s", form)
	}
	return nil
}

func (s *session) neighbours(rest []string, next func(*graph.Index, string) []string) error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	ids, err := ix.Resolve(rest[1])
	if err != nil {
		return err
	}
	if s.asJSON {
		out := map[string][]string{}
		for _, id := range ids {
			out[id] = next(ix, id)
		}
		return s.printJSON(out)
	}
	for _, id := range ids {
		f := ix.Func(id)
		fmt.Printf("%s  %s:%d\n", id, f.File, f.Line)
		if items := next(ix, id); len(items) > 0 {
			printFuncs(ix, items)
		} else {
			fmt.Println("  none")
		}
	}
	return nil
}

func (s *session) cmdImpact(rest []string) error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	ids, err := ix.Resolve(rest[1])
	if err != nil {
		return err
	}
	im := ix.Impact(ids)
	if s.asJSON {
		return s.printJSON(im)
	}
	fmt.Printf("Changing %s can affect:\n\n", strings.Join(ids, ", "))
	printImpact(ix, im)
	return nil
}

func (s *session) cmdPath(rest []string) error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	from, err := ix.Resolve(rest[1])
	if err != nil {
		return err
	}
	to, err := ix.Resolve(rest[2])
	if err != nil {
		return err
	}
	chain := ix.Path(from, to)
	if s.asJSON {
		return s.printJSON(chain)
	}
	if chain == nil {
		fmt.Println("No path found.")
		return nil
	}
	for i, id := range chain {
		f := ix.Func(id)
		fmt.Printf("%s%s  %s:%d\n", strings.Repeat("  ", i), id, f.File, f.Line)
	}
	fmt.Println("\nThis is one shortest chain. Others may exist, and it may not be the one most requests take.")
	return nil
}

func (s *session) cmdTables(rest []string) error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	ids, err := ix.Resolve(rest[1])
	if err != nil {
		return err
	}
	tables := ix.TablesReached(ids)
	if s.asJSON {
		return s.printJSON(tables)
	}
	if len(tables) == 0 {
		fmt.Println("No tables found.")
		return nil
	}
	names := make([]string, 0, len(tables))
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		via := tables[t]
		note := ""
		if len(via) > 3 {
			note = fmt.Sprintf(" and %d more", len(via)-3)
			via = via[:3]
		}
		fmt.Printf("  %s\n    via %s%s\n", t, strings.Join(via, ", "), note)
	}
	return nil
}

func (s *session) cmdRoutes(rest []string) error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	q := ""
	if len(rest) > 1 {
		q = rest[1]
	}
	routes := ix.RoutesMatching(q)
	if s.asJSON {
		return s.printJSON(routes)
	}
	if len(routes) == 0 {
		fmt.Println("No routes found. Routes are read from go-chi registrations.")
		return nil
	}
	printRoutes(routes)
	return nil
}

func (s *session) cmdFind(rest []string) error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	found := ix.Find(rest[1])
	if s.asJSON {
		return s.printJSON(found)
	}
	for _, f := range found {
		fmt.Printf("%s  %s:%d\n", f.ID, f.File, f.Line)
		if f.Doc != "" {
			fmt.Printf("    %s\n", f.Doc)
		}
	}
	if len(found) == 0 {
		fmt.Println("Nothing found.")
	}
	return nil
}

func (s *session) warn(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "repograph: warning:", w)
	}
}

// changesSince compares ref with the code as it is now, uncommitted work included.
func (s *session) changesSince(ref string) (*graph.Index, graph.Changes, error) {
	before, warnings, err := gitref.Build(s.root, ref, s.tags)
	if err != nil {
		return nil, graph.Changes{}, err
	}
	s.warn(warnings)
	ix, err := s.index()
	if err != nil {
		return nil, graph.Changes{}, err
	}
	return ix, graph.Diff(before, ix.Graph), nil
}

func (s *session) cmdAffected(rest []string) error {
	ix, c, err := s.changesSince(rest[1])
	if err != nil {
		return err
	}
	changed := c.ChangedProductionCode(ix)
	im := ix.Impact(changed)
	if s.asJSON {
		return s.printJSON(struct {
			Changed []string     `json:"changed"`
			Removed []string     `json:"removed,omitempty"`
			Impact  graph.Impact `json:"impact"`
		}{changed, c.FuncsRemoved, im})
	}
	if len(changed) == 0 && len(c.FuncsRemoved) == 0 {
		fmt.Printf("No code changes since %s, so nothing is affected. Test-only and comment-only edits don't count.\n", rest[1])
		return nil
	}
	fmt.Printf("Changed since %s (%d):\n", rest[1], len(changed))
	printFuncs(ix, changed)
	if len(c.FuncsRemoved) > 0 {
		fmt.Printf("\nRemoved (%d):\n", len(c.FuncsRemoved))
		for _, id := range c.FuncsRemoved {
			fmt.Printf("  %s\n", id)
		}
	}
	fmt.Println()
	printImpact(ix, im)
	return nil
}

func (s *session) cmdDiff(rest []string) error {
	var ix *graph.Index
	var c graph.Changes
	if len(rest) == 3 {
		a, err := graph.Load(rest[1])
		if err != nil {
			return err
		}
		b, err := graph.Load(rest[2])
		if err != nil {
			return err
		}
		ix, c = graph.NewIndex(b), graph.Diff(a, b)
	} else {
		var err error
		ix, c, err = s.changesSince(rest[1])
		if err != nil {
			return err
		}
	}
	if s.asJSON {
		return s.printJSON(c)
	}
	if c.Empty() {
		fmt.Println("No code changes.")
		return nil
	}
	printChanges(ix, c)
	return nil
}

func (s *session) cmdBuild() error {
	if s.out != "" {
		g, warnings, err := extract.Build(s.root, s.tags)
		if err != nil {
			return err
		}
		s.warn(warnings)
		if err := g.Save(s.out); err != nil {
			return err
		}
		fmt.Printf("Saved %d functions and %d routes to %s\n", len(g.Funcs), len(g.Routes), s.out)
		return nil
	}
	ix, err := s.index()
	if err != nil {
		return err
	}
	fmt.Printf("%d functions, %d routes\n", len(ix.Funcs), len(ix.Routes))
	return nil
}

func (s *session) cmdCheck(rest []string) error {
	saved, err := graph.Load(rest[1])
	if err != nil {
		return err
	}
	ix, err := s.index()
	if err != nil {
		return err
	}
	c := graph.Diff(saved, ix.Graph)
	if c.Empty() {
		fmt.Println("The saved graph matches the code.")
		return nil
	}
	printChanges(ix, c)
	fmt.Printf("%s is out of date. Run: repograph build -o %s\n", rest[1], rest[1])
	return errFailedCheck
}

func (s *session) cmdDocs(rest []string) error {
	problems, checked, err := docs.Check(s.root, rest[1:])
	if err != nil {
		return err
	}
	if s.asJSON {
		if err := s.printJSON(problems); err != nil {
			return err
		}
	} else {
		for _, p := range problems {
			fmt.Printf("%s:%d: %s no longer exists\n", p.File, p.Line, p.Ref)
		}
		fmt.Printf("%d references checked, %d missing\n", checked, len(problems))
	}
	if len(problems) > 0 {
		return errFailedCheck
	}
	return nil
}

func (s *session) cmdDiagram() error {
	ix, err := s.index()
	if err != nil {
		return err
	}
	diagram := docs.PackageDiagram(ix.Graph)
	file := s.write
	if file == "" {
		file = s.chk
	}
	if file == "" {
		fmt.Println(diagram)
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	updated, err := docs.ReplaceDiagram(string(b), diagram)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if s.chk != "" {
		if updated != string(b) {
			fmt.Printf("The diagram in %s is out of date. Run: repograph diagram -write %s\n", file, file)
			return errFailedCheck
		}
		fmt.Println("The diagram is up to date.")
		return nil
	}
	if updated == string(b) {
		fmt.Println("The diagram is already up to date.")
		return nil
	}
	if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
		return err
	}
	fmt.Printf("Updated the diagram in %s\n", file)
	return nil
}

func run(args []string) error {
	o, rest, err := parseArgs(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("no command given\n\n" + usage)
	}
	if rest[0] == "help" {
		fmt.Print(usage)
		return nil
	}

	forms := map[string]struct {
		args int
		form string
	}{
		"impact": {1, "impact <name>"}, "callers": {1, "callers <name>"}, "callees": {1, "callees <name>"},
		"path": {2, "path <from> <to>"}, "tables": {1, "tables <name>"}, "routes": {0, "routes [text]"},
		"find": {1, "find <text>"}, "affected": {1, "affected <ref>"}, "diff": {1, "diff <ref> | diff <a.json> <b.json>"},
		"docs": {1, "docs <file.md>..."}, "diagram": {0, "diagram [-write <f.md> | -check <f.md>]"},
		"build": {0, "build [-o <file.json>]"}, "check": {1, "check <file.json>"},
	}
	cmd := rest[0]
	f, ok := forms[cmd]
	if !ok {
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
	if err := need(rest, f.args, f.form); err != nil {
		return err
	}

	// Paths given to a command are relative to where it was started, not to -C.
	root, err := cache.FindRoot(o.dir)
	if err != nil {
		return err
	}
	if root, err = filepath.Abs(root); err != nil {
		return err
	}
	s := &session{options: o, root: root}

	switch cmd {
	case "impact":
		return s.cmdImpact(rest)
	case "callers":
		return s.neighbours(rest, func(ix *graph.Index, id string) []string { return ix.Callers(id) })
	case "callees":
		return s.neighbours(rest, func(ix *graph.Index, id string) []string { return ix.Callees(id) })
	case "path":
		return s.cmdPath(rest)
	case "tables":
		return s.cmdTables(rest)
	case "routes":
		return s.cmdRoutes(rest)
	case "find":
		return s.cmdFind(rest)
	case "affected":
		return s.cmdAffected(rest)
	case "diff":
		return s.cmdDiff(rest)
	case "docs":
		return s.cmdDocs(rest)
	case "diagram":
		return s.cmdDiagram()
	case "build":
		return s.cmdBuild()
	case "check":
		return s.cmdCheck(rest)
	}
	return nil
}

func main() {
	err := run(os.Args[1:])
	switch {
	case err == nil:
	case errors.Is(err, errFailedCheck):
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "repograph:", err)
		os.Exit(2)
	}
}
