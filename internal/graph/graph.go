// Package graph holds the repository graph and answers questions about it.
package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// FormatVersion changes whenever the saved format does, so an old graph file is
// rebuilt or rejected instead of being misread.
const FormatVersion = 1

// KindVar marks a package-level variable. It is a node only when its value refers
// to functions, as a table of handlers does, so that calls through it can be followed.
const KindVar = "var"

type Func struct {
	ID   string `json:"id"`
	Pkg  string `json:"pkg"`
	File string `json:"file"`
	Line int    `json:"line"`
	Doc  string `json:"doc,omitempty"`
	Kind string `json:"kind,omitempty"`
	Test bool   `json:"test,omitempty"`
	// Hash covers the code but not comments or layout, so it changes only when
	// the function really does.
	Hash string `json:"hash,omitempty"`
}

type Route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type Graph struct {
	Version int                 `json:"version"`
	Module  string              `json:"module"`
	Imports map[string][]string `json:"imports"`
	Funcs   []Func              `json:"funcs"`
	Calls   map[string][]string `json:"calls"`
	Routes  []Route             `json:"routes"`
	Tables  map[string][]string `json:"tables"`
}

// Save writes the graph as stable, diff-friendly JSON.
func (g *Graph) Save(path string) error {
	g.Version = FormatVersion
	return g.saveRaw(path)
}

func (g *Graph) saveRaw(path string) error {
	b, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Load reads a saved graph.
func Load(path string) (*Graph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	g := &Graph{}
	if err := json.Unmarshal(b, g); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if g.Version != FormatVersion {
		return nil, fmt.Errorf("%s has format version %d, this repograph reads version %d; rebuild it", path, g.Version, FormatVersion)
	}
	return g, nil
}

// Index makes a graph quick to query.
type Index struct {
	*Graph
	byID map[string]Func
	rev  map[string][]string
}

func NewIndex(g *Graph) *Index {
	ix := &Index{Graph: g, byID: make(map[string]Func, len(g.Funcs)), rev: map[string][]string{}}
	for _, f := range g.Funcs {
		ix.byID[f.ID] = f
	}
	for from, tos := range g.Calls {
		for _, to := range tos {
			ix.rev[to] = append(ix.rev[to], from)
		}
	}
	for k := range ix.rev {
		sort.Strings(ix.rev[k])
	}
	return ix
}

// ShortName is the part of an ID a person would type: Search for (*store.Store).Search.
func ShortName(id string) string {
	return id[strings.LastIndex(id, ".")+1:]
}

// Func returns the node for an ID.
func (ix *Index) Func(id string) Func { return ix.byID[id] }

// Resolve turns what a person typed into IDs. A bare name can match several
// functions. Test doubles are left out when production code has the same name.
func (ix *Index) Resolve(q string) ([]string, error) {
	if _, ok := ix.byID[q]; ok {
		return []string{q}, nil
	}
	var prod, test, similar []string
	for _, f := range ix.Funcs {
		switch {
		case ShortName(f.ID) == q && f.Test:
			test = append(test, f.ID)
		case ShortName(f.ID) == q:
			prod = append(prod, f.ID)
		case !f.Test && strings.Contains(strings.ToLower(f.ID), strings.ToLower(q)):
			similar = append(similar, f.ID)
		}
	}
	if len(prod) > 0 {
		sort.Strings(prod)
		return prod, nil
	}
	if len(test) > 0 {
		sort.Strings(test)
		return test, nil
	}
	if len(similar) > 0 {
		sort.Strings(similar)
		if len(similar) > 8 {
			similar = append(similar[:8], "...")
		}
		return nil, fmt.Errorf("no function named %q. Did you mean: %s", q, strings.Join(similar, ", "))
	}
	return nil, fmt.Errorf("no function named %q", q)
}

// Callers lists everything that refers to id, tests included.
func (ix *Index) Callers(id string) []string { return ix.rev[id] }

// Callees lists everything id refers to.
func (ix *Index) Callees(id string) []string { return ix.Calls[id] }

func (ix *Index) isTest(id string) bool { return ix.byID[id].Test }

// walk visits production code from starts, returning everything reached.
func (ix *Index) walk(starts []string, next func(string) []string) map[string]bool {
	seen := map[string]bool{}
	queue := slices.Clone(starts)
	for _, s := range starts {
		seen[s] = true
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range next(cur) {
			if !seen[n] && !ix.isTest(n) {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return seen
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Impact is what a change to some functions can affect.
type Impact struct {
	Targets     []string `json:"targets"`
	Routes      []Route  `json:"routes"`
	TotalRoutes int      `json:"totalRoutes"`
	Reaching    []string `json:"reaching"`
	Direct      []string `json:"direct"`
	Tests       []string `json:"tests"`
}

// Impact walks callers up to the routes. It is an upper bound: a path existing in
// the code doesn't mean every request takes it.
func (ix *Index) Impact(targets []string) Impact {
	reached := ix.walk(targets, func(id string) []string { return ix.rev[id] })
	im := Impact{Targets: targets, TotalRoutes: len(ix.Routes)}
	for _, r := range ix.Routes {
		if reached[r.Handler] {
			im.Routes = append(im.Routes, r)
		}
	}
	isTarget := map[string]bool{}
	for _, t := range targets {
		isTarget[t] = true
	}
	for _, id := range sortedKeys(reached) {
		if !isTarget[id] {
			im.Reaching = append(im.Reaching, id)
		}
	}
	direct := map[string]bool{}
	for _, t := range targets {
		for _, c := range ix.rev[t] {
			if !ix.isTest(c) && !isTarget[c] {
				direct[c] = true
			}
		}
	}
	im.Direct = sortedKeys(direct)
	im.Tests = ix.testsOf(targets)
	return im
}

// testsOf finds test functions that use a target directly or through a test helper.
func (ix *Index) testsOf(targets []string) []string {
	seen := map[string]bool{}
	tests := map[string]bool{}
	queue := slices.Clone(targets)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range ix.rev[cur] {
			if seen[c] || !ix.isTest(c) {
				continue
			}
			seen[c] = true
			queue = append(queue, c)
			if isTestName(ShortName(c)) {
				tests[c] = true
			}
		}
	}
	return sortedKeys(tests)
}

func isTestName(name string) bool {
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Path returns one shortest chain through production code, or nil. Others of the
// same length may exist, and the shortest isn't always the one requests usually take.
func (ix *Index) Path(from, to []string) []string {
	want := map[string]bool{}
	for _, t := range to {
		want[t] = true
	}
	prev := map[string]string{}
	queue := slices.Clone(from)
	for _, s := range from {
		prev[s] = ""
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if want[cur] {
			var chain []string
			for c := cur; c != ""; c = prev[c] {
				chain = append(chain, c)
			}
			slices.Reverse(chain)
			return chain
		}
		for _, n := range ix.Calls[cur] {
			if _, ok := prev[n]; !ok && !ix.isTest(n) {
				prev[n] = cur
				queue = append(queue, n)
			}
		}
	}
	return nil
}

// TablesReached maps each table to the functions that name it, following calls
// down from the targets.
func (ix *Index) TablesReached(targets []string) map[string][]string {
	out := map[string][]string{}
	for _, f := range sortedKeys(ix.walk(targets, func(id string) []string { return ix.Calls[id] })) {
		for _, t := range ix.Tables[f] {
			out[t] = append(out[t], f)
		}
	}
	return out
}

// Find searches production function names and doc comments.
func (ix *Index) Find(q string) []Func {
	q = strings.ToLower(q)
	var out []Func
	for _, f := range ix.Funcs {
		if !f.Test && (strings.Contains(strings.ToLower(f.ID), q) || strings.Contains(strings.ToLower(f.Doc), q)) {
			out = append(out, f)
		}
	}
	return out
}

// RoutesMatching lists routes whose path or handler contains q.
func (ix *Index) RoutesMatching(q string) []Route {
	q = strings.ToLower(q)
	var out []Route
	for _, r := range ix.Routes {
		if strings.Contains(strings.ToLower(r.Method+" "+r.Path+" "+r.Handler), q) {
			out = append(out, r)
		}
	}
	return out
}

// Changes lists what differs between two graphs. Moving a function or rewording
// its comment isn't a change.
type Changes struct {
	FuncsAdded     []string `json:"funcsAdded,omitempty"`
	FuncsRemoved   []string `json:"funcsRemoved,omitempty"`
	FuncsChanged   []string `json:"funcsChanged,omitempty"`
	CallsAdded     []string `json:"callsAdded,omitempty"`
	CallsRemoved   []string `json:"callsRemoved,omitempty"`
	RoutesAdded    []string `json:"routesAdded,omitempty"`
	RoutesRemoved  []string `json:"routesRemoved,omitempty"`
	TablesAdded    []string `json:"tablesAdded,omitempty"`
	TablesRemoved  []string `json:"tablesRemoved,omitempty"`
	ImportsAdded   []string `json:"importsAdded,omitempty"`
	ImportsRemoved []string `json:"importsRemoved,omitempty"`
}

// Empty reports whether nothing changed.
func (c Changes) Empty() bool {
	return len(c.FuncsAdded)+len(c.FuncsRemoved)+len(c.FuncsChanged)+len(c.CallsAdded)+len(c.CallsRemoved)+
		len(c.RoutesAdded)+len(c.RoutesRemoved)+len(c.TablesAdded)+len(c.TablesRemoved)+
		len(c.ImportsAdded)+len(c.ImportsRemoved) == 0
}

func edges(m map[string][]string) map[string]bool {
	out := map[string]bool{}
	for k, vs := range m {
		for _, v := range vs {
			out[k+" -> "+v] = true
		}
	}
	return out
}

func routeFacts(rs []Route) map[string]bool {
	out := map[string]bool{}
	for _, r := range rs {
		out[r.Method+" "+r.Path+" -> "+r.Handler] = true
	}
	return out
}

func compare(a, b map[string]bool) (added, removed []string) {
	for _, k := range sortedKeys(b) {
		if !a[k] {
			added = append(added, k)
		}
	}
	for _, k := range sortedKeys(a) {
		if !b[k] {
			removed = append(removed, k)
		}
	}
	return added, removed
}

// Diff compares two graphs.
func Diff(a, b *Graph) Changes {
	var c Changes
	af, bf := map[string]Func{}, map[string]Func{}
	aIDs, bIDs := map[string]bool{}, map[string]bool{}
	for _, f := range a.Funcs {
		af[f.ID], aIDs[f.ID] = f, true
	}
	for _, f := range b.Funcs {
		bf[f.ID], bIDs[f.ID] = f, true
	}
	c.FuncsAdded, c.FuncsRemoved = compare(aIDs, bIDs)
	for _, id := range sortedKeys(aIDs) {
		if other, ok := bf[id]; ok && other.Hash != af[id].Hash {
			c.FuncsChanged = append(c.FuncsChanged, id)
		}
	}
	c.CallsAdded, c.CallsRemoved = compare(edges(a.Calls), edges(b.Calls))
	c.RoutesAdded, c.RoutesRemoved = compare(routeFacts(a.Routes), routeFacts(b.Routes))
	c.TablesAdded, c.TablesRemoved = compare(edges(a.Tables), edges(b.Tables))
	c.ImportsAdded, c.ImportsRemoved = compare(edges(a.Imports), edges(b.Imports))
	return c
}

// RemovedProductionCode lists removed functions, leaving test code out.
func (c Changes) RemovedProductionCode(before *Index) []string {
	var out []string
	for _, id := range c.FuncsRemoved {
		if !before.isTest(id) {
			out = append(out, id)
		}
	}
	return out
}

// ChangedProductionCode lists the changed and added functions in the new graph,
// leaving test code out. Removed functions aren't listed: their callers had to
// change too, so those callers already are.
func (c Changes) ChangedProductionCode(after *Index) []string {
	var out []string
	for _, id := range append(slices.Clone(c.FuncsChanged), c.FuncsAdded...) {
		if !after.isTest(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
