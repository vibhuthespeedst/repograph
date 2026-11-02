package cache

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vibhuthespeedst/repograph/internal/graph"
)

const sample = "../extract/testdata/sample"

func copySample(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(sample)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func load(t *testing.T, dir string, tags ...string) Result {
	t.Helper()
	r, err := Load(dir, tags)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hasFunc(g *graph.Graph, id string) bool {
	return slices.ContainsFunc(g.Funcs, func(f graph.Func) bool { return f.ID == id })
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestSecondLoadUsesTheCache(t *testing.T) {
	dir := copySample(t)
	if !load(t, dir).Rebuilt {
		t.Error("the first load has to build")
	}
	r := load(t, dir)
	if r.Rebuilt {
		t.Error("nothing changed, so the second load should not rebuild")
	}
	if !hasFunc(r.Graph, "(*store.DB).Get") {
		t.Error("the cached graph should still have the functions")
	}
}

func TestEditingCodeIsPickedUpWithoutAskingForARebuild(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	appendTo(t, filepath.Join(dir, "store", "store.go"), "\nfunc Fresh() bool { return isID(\"x\") }\n")
	r := load(t, dir)
	if !r.Rebuilt {
		t.Error("an edit to a Go file should trigger a rebuild")
	}
	if !hasFunc(r.Graph, "store.Fresh") {
		t.Error("the new function should be in the graph")
	}
	if !slices.Contains(r.Graph.Calls["store.Fresh"], "store.isID") {
		t.Errorf("the new call should be in the graph: %v", r.Graph.Calls["store.Fresh"])
	}
}

func TestDeletingAFileIsPickedUp(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	if err := os.Remove(filepath.Join(dir, "store", "store_test.go")); err != nil {
		t.Fatal(err)
	}
	r := load(t, dir)
	if !r.Rebuilt || hasFunc(r.Graph, "store.TestGet") {
		t.Errorf("Rebuilt = %v; store.TestGet should be gone", r.Rebuilt)
	}
}

func TestChangingASchemaIsPickedUp(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	appendTo(t, filepath.Join(dir, "schema.sql"), "\nCREATE TABLE extra_table (id TEXT);\n")
	if !load(t, dir).Rebuilt {
		t.Error("a schema change should trigger a rebuild")
	}
}

func TestUnrelatedFilesDoNotTriggerARebuild(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if load(t, dir).Rebuilt {
		t.Error("a Markdown file shouldn't trigger a rebuild")
	}
}

func TestDifferentTagsGetTheirOwnGraph(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	r := load(t, dir, "extra")
	if !r.Rebuilt || !hasFunc(r.Graph, "store.Extra") {
		t.Errorf("Rebuilt = %v; a different tag set should rebuild and find store.Extra", r.Rebuilt)
	}
}

func TestCorruptCacheIsRebuilt(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	if err := os.WriteFile(filepath.Join(dir, Dir, "graph.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := load(t, dir)
	if !r.Rebuilt || !hasFunc(r.Graph, "(*store.DB).Get") {
		t.Errorf("Rebuilt = %v; a damaged cache should be replaced, not fail", r.Rebuilt)
	}
}

func TestCacheDirectoryIgnoresItself(t *testing.T) {
	dir := copySample(t)
	load(t, dir)
	b, err := os.ReadFile(filepath.Join(dir, Dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "*" {
		t.Errorf(".gitignore = %q, want it to ignore everything so the repo stays clean", b)
	}
}

func TestBrokenCodeIsNotCached(t *testing.T) {
	dir := copySample(t)
	if err := os.WriteFile(filepath.Join(dir, "api", "broken.go"), []byte("package api\n\nfunc Broken() { nope() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := load(t, dir); len(r.Warnings) == 0 {
		t.Error("expected a warning")
	}
	// The warning must show every time, not only on the run that first saw it.
	if r := load(t, dir); !r.Rebuilt || len(r.Warnings) == 0 {
		t.Errorf("Rebuilt = %v, warnings = %v; a partial graph shouldn't be cached", r.Rebuilt, r.Warnings)
	}
}

func TestFindRootWalksUpToGoMod(t *testing.T) {
	dir := copySample(t)
	got, err := FindRoot(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if gotResolved, _ := filepath.EvalSymlinks(got); gotResolved != want {
		t.Errorf("FindRoot = %q, want %q", got, want)
	}
	if _, err := FindRoot(t.TempDir()); err == nil {
		t.Error("a directory with no go.mod should be an error")
	}
}
