package graph

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sample is a small graph: two routes reach store.Get through an interface, and a
// test reaches it directly.
func sample() *Graph {
	return &Graph{
		Module: "example.com/x",
		Funcs: []Func{
			{ID: "(*api.H).get", Pkg: "api", File: "api/api.go", Line: 1, Hash: "a"},
			{ID: "(*api.H).list", Pkg: "api", File: "api/api.go", Line: 2, Hash: "b"},
			{ID: "(*api.H).other", Pkg: "api", File: "api/api.go", Line: 3, Hash: "c"},
			{ID: "(api.Store).Get", Pkg: "api", File: "api/api.go", Line: 4},
			{ID: "(*store.DB).Get", Pkg: "store", File: "store/db.go", Line: 5, Doc: "Get loads a row.", Hash: "d"},
			{ID: "store.query", Pkg: "store", File: "store/db.go", Line: 6, Hash: "e"},
			{ID: "store.TestGet", Pkg: "store", File: "store/db_test.go", Line: 7, Test: true, Hash: "f"},
			{ID: "store.helper", Pkg: "store", File: "store/db_test.go", Line: 8, Test: true, Hash: "g"},
			{ID: "(*fake.DB).Get", Pkg: "fake", File: "fake/fake_test.go", Line: 9, Test: true, Hash: "h"},
		},
		Calls: map[string][]string{
			"(*api.H).get":    {"(api.Store).Get"},
			"(*api.H).list":   {"(api.Store).Get"},
			"(api.Store).Get": {"(*store.DB).Get"},
			"(*store.DB).Get": {"store.query"},
			"store.TestGet":   {"(*store.DB).Get", "store.helper"},
		},
		Routes: []Route{
			{Method: "GET", Path: "/a", Handler: "(*api.H).get"},
			{Method: "GET", Path: "/b", Handler: "(*api.H).list"},
			{Method: "GET", Path: "/c", Handler: "(*api.H).other"},
		},
		Tables:  map[string][]string{"store.query": {"orders"}},
		Imports: map[string][]string{"api": {"store"}},
	}
}

func TestResolve(t *testing.T) {
	ix := NewIndex(sample())
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"(*store.DB).Get", []string{"(*store.DB).Get"}},
		{"query", []string{"store.query"}},
		// A bare name leaves test doubles out when production code has the name.
		{"Get", []string{"(*store.DB).Get", "(api.Store).Get"}},
		// A test can still be asked for by name.
		{"TestGet", []string{"store.TestGet"}},
	} {
		got, err := ix.Resolve(tc.q)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tc.q, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Resolve(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestResolveUnknownSuggestsSimilarNames(t *testing.T) {
	_, err := NewIndex(sample()).Resolve("quer")
	if err == nil || !strings.Contains(err.Error(), "store.query") {
		t.Errorf("err = %v, want a suggestion of store.query", err)
	}
	_, err = NewIndex(sample()).Resolve("zzz")
	if err == nil || !strings.Contains(err.Error(), "zzz") {
		t.Errorf("err = %v, want it to name what was searched for", err)
	}
}

func TestCallersAndCallees(t *testing.T) {
	ix := NewIndex(sample())
	if got, want := ix.Callers("(*store.DB).Get"), []string{"(api.Store).Get", "store.TestGet"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Callers = %v, want %v", got, want)
	}
	if got, want := ix.Callees("(*store.DB).Get"), []string{"store.query"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Callees = %v, want %v", got, want)
	}
}

func TestImpactWalksUpToRoutes(t *testing.T) {
	im := NewIndex(sample()).Impact([]string{"store.query"})
	var routes []string
	for _, r := range im.Routes {
		routes = append(routes, r.Path)
	}
	if want := []string{"/a", "/b"}; !reflect.DeepEqual(routes, want) {
		t.Errorf("routes = %v, want %v", routes, want)
	}
	if im.TotalRoutes != 3 {
		t.Errorf("TotalRoutes = %d", im.TotalRoutes)
	}
	want := []string{"(*api.H).get", "(*api.H).list", "(*store.DB).Get", "(api.Store).Get"}
	if !reflect.DeepEqual(im.Reaching, want) {
		t.Errorf("Reaching = %v, want %v", im.Reaching, want)
	}
	if !reflect.DeepEqual(im.Direct, []string{"(*store.DB).Get"}) {
		t.Errorf("Direct = %v", im.Direct)
	}
}

func TestImpactLeavesOutTestsOfCallers(t *testing.T) {
	ix := NewIndex(sample())
	if got := ix.Impact([]string{"(*store.DB).Get"}).Tests; !reflect.DeepEqual(got, []string{"store.TestGet"}) {
		t.Errorf("Tests = %v", got)
	}
	if got := ix.Impact([]string{"store.query"}).Tests; len(got) != 0 {
		t.Errorf("store.query has no direct test, got %v", got)
	}
}

func TestImpactFindsTestsThroughHelpers(t *testing.T) {
	g := sample()
	g.Calls["store.helper"] = []string{"store.query"}
	if got := NewIndex(g).Impact([]string{"store.query"}).Tests; !reflect.DeepEqual(got, []string{"store.TestGet"}) {
		t.Errorf("Tests = %v", got)
	}
}

func TestPathGoesThroughInterfaces(t *testing.T) {
	got := NewIndex(sample()).Path([]string{"(*api.H).get"}, []string{"store.query"})
	want := []string{"(*api.H).get", "(api.Store).Get", "(*store.DB).Get", "store.query"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Path = %v, want %v", got, want)
	}
}

func TestPathNone(t *testing.T) {
	if got := NewIndex(sample()).Path([]string{"store.query"}, []string{"(*api.H).get"}); got != nil {
		t.Errorf("Path = %v, want none", got)
	}
}

func TestPathSkipsTestCode(t *testing.T) {
	g := sample()
	g.Calls["(*api.H).other"] = []string{"store.helper"}
	g.Calls["store.helper"] = []string{"store.query"}
	if got := NewIndex(g).Path([]string{"(*api.H).other"}, []string{"store.query"}); got != nil {
		t.Errorf("Path = %v, should not pass through test helpers", got)
	}
}

func TestTablesReachedFollowsCalls(t *testing.T) {
	got := NewIndex(sample()).TablesReached([]string{"(*api.H).get"})
	if want := map[string][]string{"orders": {"store.query"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("TablesReached = %v, want %v", got, want)
	}
}

func TestFindSearchesNamesAndDocsButNotTests(t *testing.T) {
	ix := NewIndex(sample())
	var byDoc []string
	for _, f := range ix.Find("loads a ROW") {
		byDoc = append(byDoc, f.ID)
	}
	if !reflect.DeepEqual(byDoc, []string{"(*store.DB).Get"}) {
		t.Errorf("by doc = %v", byDoc)
	}
	for _, f := range ix.Find("TestGet") {
		t.Errorf("Find should skip tests, got %s", f.ID)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := sample().Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Calls, sample().Calls) || len(got.Funcs) != len(sample().Funcs) {
		t.Error("graph changed after a round trip")
	}
}

func TestLoadRejectsAnOlderFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	g := sample()
	g.Version = FormatVersion + 1
	if err := g.saveRaw(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("a graph in another format should be rejected")
	}
}

func TestDiffNoChanges(t *testing.T) {
	if d := Diff(sample(), sample()); !d.Empty() {
		t.Errorf("Diff of identical graphs: %+v", d)
	}
}

func TestDiffIgnoresMovedLines(t *testing.T) {
	moved := sample()
	moved.Funcs[0].Line = 500
	moved.Funcs[0].Doc = "new words"
	if d := Diff(sample(), moved); !d.Empty() {
		t.Errorf("moving a function or editing its comment is not a change: %+v", d)
	}
}

func TestDiffReportsEachKindOfChange(t *testing.T) {
	after := sample()
	after.Funcs[5].Hash = "changed"                                                     // store.query
	after.Funcs = append(after.Funcs, Func{ID: "store.fresh", Pkg: "store", Hash: "z"}) // added
	after.Funcs = append(after.Funcs[:2], after.Funcs[3:]...)                           // (*api.H).other removed
	after.Calls["store.query"] = []string{"store.fresh"}
	after.Routes = after.Routes[:2]
	after.Tables["store.fresh"] = []string{"audit"}
	after.Imports["store"] = []string{"fake"}

	d := Diff(sample(), after)
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("FuncsAdded", d.FuncsAdded, []string{"store.fresh"})
	check("FuncsRemoved", d.FuncsRemoved, []string{"(*api.H).other"})
	check("FuncsChanged", d.FuncsChanged, []string{"store.query"})
	check("CallsAdded", d.CallsAdded, []string{"store.query -> store.fresh"})
	check("RoutesRemoved", d.RoutesRemoved, []string{"GET /c -> (*api.H).other"})
	check("TablesAdded", d.TablesAdded, []string{"store.fresh -> audit"})
	check("ImportsAdded", d.ImportsAdded, []string{"store -> fake"})
	if d.Empty() {
		t.Error("Empty() should be false")
	}
}
