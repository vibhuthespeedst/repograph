package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibhuthespeedst/repograph/internal/graph"
)

const sample = "testdata/sample"

func build(t *testing.T, dir string, tags ...string) *graph.Graph {
	t.Helper()
	g, warnings, err := Build(dir, tags)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	return g
}

func funcByID(g *graph.Graph, id string) (graph.Func, bool) {
	for _, f := range g.Funcs {
		if f.ID == id {
			return f, true
		}
	}
	return graph.Func{}, false
}

func wantEdge(t *testing.T, g *graph.Graph, from, to string) {
	t.Helper()
	if !slices.Contains(g.Calls[from], to) {
		t.Errorf("missing edge %s -> %s; %s has %v", from, to, from, g.Calls[from])
	}
}

func TestFindsFunctionsWithLocationAndDoc(t *testing.T) {
	g := build(t, sample)
	f, ok := funcByID(g, "(*store.DB).Get")
	if !ok {
		t.Fatal("(*store.DB).Get not found")
	}
	if f.File != "store/store.go" || f.Line != 10 || f.Pkg != "store" {
		t.Errorf("wrong location: %+v", f)
	}
	if f.Doc != "Get loads one order." {
		t.Errorf("Doc = %q", f.Doc)
	}
	if g.Module != "example.com/sample" {
		t.Errorf("Module = %q", g.Module)
	}
}

func TestCallsAreResolved(t *testing.T) {
	g := build(t, sample)
	wantEdge(t, g, "(*store.DB).Save", "(*store.DB).audit")
	wantEdge(t, g, "(*store.DB).Save", "store.Validate")
	wantEdge(t, g, "main.main", "api.New")
}

func TestInterfaceMethodLeadsToImplementation(t *testing.T) {
	g := build(t, sample)
	wantEdge(t, g, "(*api.Handler).get", "(api.Store).Get")
	wantEdge(t, g, "(api.Store).Get", "(*store.DB).Get")
}

func TestMethodValuePassedToRouterIsAnEdge(t *testing.T) {
	wantEdge(t, build(t, sample), "api.New", "(*api.Handler).get")
}

func TestGenericCallResolvesToOneFunction(t *testing.T) {
	wantEdge(t, build(t, sample), "store.upper", "store.Map")
}

func TestPackageLevelVarCarriesReferences(t *testing.T) {
	g := build(t, sample)
	wantEdge(t, g, "store.validators", "store.isID")
	wantEdge(t, g, "store.Validate", "store.validators")
	if f, ok := funcByID(g, "store.validators"); !ok || f.Kind != graph.KindVar {
		t.Errorf("store.validators should be a var node, got %+v", f)
	}
}

func TestVarWithoutReferencesIsLeftOut(t *testing.T) {
	g := build(t, sample)
	if _, ok := funcByID(g, "store.unrelated"); ok {
		t.Error("store.unrelated refers to nothing and should not be a node")
	}
	if slices.Contains(g.Calls["store.Validate"], "store.unrelated") {
		t.Error("store.Validate should have no edge to store.unrelated")
	}
}

func TestRoutesJoinNestedPrefixes(t *testing.T) {
	var got []string
	for _, r := range build(t, sample).Routes {
		got = append(got, r.Method+" "+r.Path+" "+r.Handler)
	}
	want := []string{
		"GET /health (inline)",
		"GET /orders (*api.Handler).list",
		"GET /orders/{id} (*api.Handler).get",
		"POST /orders/{id} (*api.Handler).save",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes:\n got %q\nwant %q", got, want)
	}
}

func TestTablesFromLiteralsAndConstants(t *testing.T) {
	g := build(t, sample)
	if got := g.Tables["(*store.DB).Get"]; !slices.Contains(got, "orders") {
		t.Errorf("Get tables = %v, want orders from a literal", got)
	}
	if got := g.Tables["(*store.DB).Save"]; !slices.Contains(got, "orders") {
		t.Errorf("Save tables = %v, want orders from a constant", got)
	}
	if got := g.Tables["(*store.DB).audit"]; !slices.Contains(got, "audit_log") {
		t.Errorf("audit tables = %v, want audit_log", got)
	}
}

func TestImportsOnlyListRepoPackages(t *testing.T) {
	g := build(t, sample)
	if got, want := g.Imports["main"], []string{"api", "store"}; !reflect.DeepEqual(got, want) {
		t.Errorf("main imports = %v, want %v", got, want)
	}
	if _, ok := g.Imports["api"]; ok {
		t.Errorf("api imports only chi and net/http, got %v", g.Imports["api"])
	}
}

func TestTestFunctionsAreMarked(t *testing.T) {
	g := build(t, sample)
	f, ok := funcByID(g, "store.TestGet")
	if !ok || !f.Test {
		t.Errorf("store.TestGet should be marked as a test: %+v", f)
	}
	wantEdge(t, g, "store.TestGet", "(*store.DB).Get")
}

func TestBuildTags(t *testing.T) {
	if _, ok := funcByID(build(t, sample), "store.Extra"); ok {
		t.Error("store.Extra should need the extra tag")
	}
	if _, ok := funcByID(build(t, sample, "extra"), "store.Extra"); !ok {
		t.Error("store.Extra should be found with the extra tag")
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, _ := json.Marshal(build(t, sample))
	b, _ := json.Marshal(build(t, sample))
	if string(a) != string(b) {
		t.Error("two builds of the same code differ")
	}
}

// copySample gives a test its own copy of the sample to edit.
func copySample(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(sample)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func editFile(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%q not in %s", old, path)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHashChangesWithCodeOnly(t *testing.T) {
	hash := func(g *graph.Graph) string {
		f, ok := funcByID(g, "(*store.DB).Save")
		if !ok || f.Hash == "" {
			t.Fatalf("no hash: %+v", f)
		}
		return f.Hash
	}
	before := hash(build(t, sample))

	layout := copySample(t)
	file := filepath.Join(layout, "store", "store.go")
	editFile(t, file, "d.audit(id)\n", "// a comment\n\n\td.audit(id)\n")
	if got := hash(build(t, layout)); got != before {
		t.Error("a comment and a blank line should not change the hash")
	}

	changed := copySample(t)
	file = filepath.Join(changed, "store", "store.go")
	editFile(t, file, `Validate("id", id)`, `Validate("other", id)`)
	if got := hash(build(t, changed)); got == before {
		t.Error("changing a literal should change the hash")
	}
}

func TestBrokenPackageIsAWarningNotAFailure(t *testing.T) {
	dir := copySample(t)
	if err := os.WriteFile(filepath.Join(dir, "api", "broken.go"), []byte("package api\n\nfunc Broken() { undefinedThing() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, warnings, err := Build(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning for the broken package")
	}
	if _, ok := funcByID(g, "(*store.DB).Get"); !ok {
		t.Error("the packages that do compile should still be in the graph")
	}
}
