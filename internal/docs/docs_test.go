package docs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibhuthespeedst/repograph/internal/graph"
)

// repo lays out a tiny repository for the checks to read.
func repo(t *testing.T, doc string) (root, docPath string) {
	t.Helper()
	root = t.TempDir()
	for path, body := range map[string]string{
		"internal/store/search.go":  "package store\n\nfunc buildBundle() {}\nfunc load_rows() {}\n",
		"internal/store/db_test.go": "package store\n\nfunc onlyInATest() {}\n",
		"schema.sql":                "CREATE TABLE schema_version (id INT);\n",
		"config.yaml":               "createTables: true\n",
		"docs/other.md":             "mentions onlyInDocs\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docPath = filepath.Join(root, "docs", "map.md")
	if err := os.WriteFile(docPath, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, docPath
}

func refs(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Ref)
	}
	return out
}

func check(t *testing.T, doc string) []string {
	t.Helper()
	root, path := repo(t, doc)
	ps, _, err := Check(root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	return refs(ps)
}

func TestValidReferencesPass(t *testing.T) {
	doc := "See `buildBundle` and `load_rows`, `buildBundle()`, `internal/store/search.go`, `search.go` and `schema_version`.\n" +
		"Also [other](./other.md) and [a section](./other.md#top).\n"
	if got := check(t, doc); got != nil {
		t.Errorf("reported %v", got)
	}
}

func TestMissingFunctionIsReportedWithItsLine(t *testing.T) {
	root, path := repo(t, "ok\n\nUse `buildBundleOld` here.\n")
	ps, checked, err := Check(root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Ref != "buildBundleOld" || ps[0].Line != 3 {
		t.Errorf("problems = %+v", ps)
	}
	if checked != 1 {
		t.Errorf("checked = %d", checked)
	}
}

func TestRenamingToALongerNameIsStillCaught(t *testing.T) {
	// The doc names build, and the code only has buildBundle: not the same word.
	if got := check(t, "Use `buildBun_dle` and `buildB`.\n"); !reflect.DeepEqual(got, []string{"buildB", "buildBun_dle"}) {
		t.Errorf("got %v", got)
	}
}

func TestMissingPathsAreReported(t *testing.T) {
	got := check(t, "`internal/store/gone.go` and `missing.go` and [gone](./gone.md).\n")
	want := []string{"./gone.md", "internal/store/gone.go", "missing.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPlainTextInBackticksIsLeftAlone(t *testing.T) {
	doc := "`string` `GET` `make test` `application/json` `_count` `:missing` `$everything` `sp_*` " +
		"`/fhir/r4` `--flag` `-tags` `W/\"3\"` `a=b` `https://x.dev/y` `.env`\n"
	if got := check(t, doc); got != nil {
		t.Errorf("reported %v", got)
	}
}

func TestNamesOnlyInTestsOrOtherDocsDoNotCount(t *testing.T) {
	if got := check(t, "`onlyInATest` and `onlyInDocs`\n"); !reflect.DeepEqual(got, []string{"onlyInATest", "onlyInDocs"}) {
		t.Errorf("got %v", got)
	}
}

func TestMermaidNamesAreCheckedButOtherCodeBlocksAreNot(t *testing.T) {
	doc := "```mermaid\nflowchart TD\n  A[\"buildBundle\"] --> B[\"goneFunction<br/>createTables\"]\n```\n\n" +
		"```bash\nrunSomethingElse `notARealName`\n```\n"
	if got := check(t, doc); !reflect.DeepEqual(got, []string{"goneFunction"}) {
		t.Errorf("got %v", got)
	}
}

func TestSeveralFiles(t *testing.T) {
	root, path := repo(t, "`goneOne`\n")
	second := filepath.Join(root, "docs", "second.md")
	if err := os.WriteFile(second, []byte("`goneTwo`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ps, _, err := Check(root, []string{path, second})
	if err != nil {
		t.Fatal(err)
	}
	if got := refs(ps); !reflect.DeepEqual(got, []string{"goneOne", "goneTwo"}) {
		t.Errorf("got %v", got)
	}
	if !strings.HasSuffix(filepath.ToSlash(ps[1].File), "docs/second.md") {
		t.Errorf("File = %q", ps[1].File)
	}
}

func TestPackageDiagramLeavesOutCommands(t *testing.T) {
	g := &graph.Graph{Imports: map[string][]string{
		"cmd/server":       {"internal/handler", "internal/store"},
		"internal/handler": {"internal/store", "internal/tenant"},
		"internal/store":   {"internal/tenant"},
	}}
	want := "```mermaid\nflowchart LR\n" +
		"    handler --> store & tenant\n" +
		"    store --> tenant\n" +
		"```"
	if got := PackageDiagram(g); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPackageDiagramKeepsCommandsWhenNothingElseImports(t *testing.T) {
	g := &graph.Graph{Imports: map[string][]string{"cmd/server": {"internal/store"}}}
	if got := PackageDiagram(g); !strings.Contains(got, `cmd_server["cmd/server"] --> store`) {
		t.Errorf("got:\n%s", got)
	}
}

func TestPackageDiagramEscapesReservedWords(t *testing.T) {
	g := &graph.Graph{Imports: map[string][]string{"internal/end": {"internal/a-b"}}}
	got := PackageDiagram(g)
	if !strings.Contains(got, `end_["end"] --> a_b["a-b"]`) {
		t.Errorf("got:\n%s", got)
	}
}

func TestReplaceDiagram(t *testing.T) {
	doc := "# Title\n\n<!-- repograph:packages -->\nold stuff\n<!-- /repograph:packages -->\n\nAfter.\n"
	got, err := ReplaceDiagram(doc, "NEW")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Title\n\n<!-- repograph:packages -->\nNEW\n<!-- /repograph:packages -->\n\nAfter.\n"
	if got != want {
		t.Errorf("got:\n%s", got)
	}
	again, _ := ReplaceDiagram(got, "NEW")
	if again != got {
		t.Error("replacing twice should change nothing")
	}
}

func TestReplaceDiagramNeedsMarkers(t *testing.T) {
	_, err := ReplaceDiagram("# No markers\n", "NEW")
	if err == nil || !strings.Contains(err.Error(), "<!-- repograph:packages -->") {
		t.Errorf("err = %v, want it to show which markers to add", err)
	}
}
