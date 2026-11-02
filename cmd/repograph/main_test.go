package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "repograph-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "repograph.exe")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func sampleCopy(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS("../../internal/extract/testdata/sample")); err != nil {
		t.Fatal(err)
	}
	return dst
}

// runCLI returns stdout, stderr and the exit code.
func runCLI(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func wantContains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output is missing %q:\n%s", w, got)
		}
	}
}

func TestImpactShowsRoutesThroughAnInterface(t *testing.T) {
	out, _, code := runCLI(t, sampleCopy(t), "impact", "audit")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "/orders/{id}", "POST", "(*api.Handler).save", "1 of 4")
}

func TestImpactFollowsAPackageLevelTable(t *testing.T) {
	out, _, _ := runCLI(t, sampleCopy(t), "impact", "isID")
	wantContains(t, out, "store.validators", "POST", "/orders/{id}", "TestIsID")
}

func TestCommandsWorkFromASubdirectory(t *testing.T) {
	out, _, code := runCLI(t, filepath.Join(sampleCopy(t), "store"), "callers", "audit")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "(*store.DB).Save")
}

func TestDashCPointsAtAnotherDirectory(t *testing.T) {
	out, _, code := runCLI(t, t.TempDir(), "-C", sampleCopy(t), "callers", "audit")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "(*store.DB).Save")
}

func TestAQueryAfterAnEditSeesTheEdit(t *testing.T) {
	dir := sampleCopy(t)
	if _, _, code := runCLI(t, dir, "routes"); code != 0 {
		t.Fatal("first run failed")
	}
	f := filepath.Join(dir, "store", "store.go")
	b, _ := os.ReadFile(f)
	if err := os.WriteFile(f, append(b, []byte("\nfunc Fresh() { unused() }\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runCLI(t, dir, "callers", "unused")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	wantContains(t, out, "store.Fresh")
	wantContains(t, errOut, "rebuilt")
}

func TestCachedRunIsQuiet(t *testing.T) {
	dir := sampleCopy(t)
	runCLI(t, dir, "routes")
	if _, errOut, _ := runCLI(t, dir, "routes"); errOut != "" {
		t.Errorf("stderr should be empty when nothing changed: %q", errOut)
	}
}

func TestPathTablesFindAndRoutes(t *testing.T) {
	dir := sampleCopy(t)
	out, _, _ := runCLI(t, dir, "path", "save", "audit")
	wantContains(t, out, "(*api.Handler).save", "(api.Store).Save", "(*store.DB).Save", "(*store.DB).audit")
	out, _, _ = runCLI(t, dir, "tables", "save")
	wantContains(t, out, "orders", "audit_log")
	if strings.Contains(out, "never_used") {
		t.Errorf("never_used shouldn't be listed:\n%s", out)
	}
	out, _, _ = runCLI(t, dir, "find", "loads one")
	wantContains(t, out, "(*store.DB).Get", "store/store.go")
	out, _, _ = runCLI(t, dir, "routes", "orders")
	wantContains(t, out, "/orders/{id}")
	if strings.Contains(out, "/health") {
		t.Errorf("the filter should leave out /health:\n%s", out)
	}
}

func TestJSONOutput(t *testing.T) {
	out, _, code := runCLI(t, sampleCopy(t), "impact", "audit", "-json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var im struct {
		Targets []string
		Routes  []struct{ Path string }
	}
	if err := json.Unmarshal([]byte(out), &im); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(im.Routes) != 1 || im.Routes[0].Path != "/orders/{id}" {
		t.Errorf("routes = %+v", im.Routes)
	}
}

func TestUnknownNameIsAnErrorWithASuggestion(t *testing.T) {
	_, errOut, code := runCLI(t, sampleCopy(t), "callers", "audi")
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	wantContains(t, errOut, "(*store.DB).audit")
}

func TestUsageAndBadInput(t *testing.T) {
	dir := sampleCopy(t)
	if out, _, code := runCLI(t, dir, "help"); code != 0 || !strings.Contains(out, "impact") {
		t.Errorf("help: exit %d\n%s", code, out)
	}
	if _, _, code := runCLI(t, dir); code != 2 {
		t.Errorf("no args: exit %d, want 2", code)
	}
	if _, _, code := runCLI(t, dir, "nonsense"); code != 2 {
		t.Errorf("unknown command: exit %d, want 2", code)
	}
	if _, _, code := runCLI(t, dir, "routes", "-nope"); code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
	if _, errOut, code := runCLI(t, dir, "callers"); code != 2 || !strings.Contains(errOut, "callers") {
		t.Errorf("missing argument: exit %d, %q", code, errOut)
	}
	if _, errOut, code := runCLI(t, t.TempDir(), "routes"); code != 2 || !strings.Contains(errOut, "go.mod") {
		t.Errorf("outside a module: exit %d, %q", code, errOut)
	}
}

func TestBuildAndCheck(t *testing.T) {
	dir := sampleCopy(t)
	saved := filepath.Join(t.TempDir(), "graph.json")
	if _, _, code := runCLI(t, dir, "build", "-o", saved); code != 0 {
		t.Fatalf("build exit %d", code)
	}
	out, _, code := runCLI(t, dir, "check", saved)
	if code != 0 {
		t.Fatalf("check on unchanged code: exit %d\n%s", code, out)
	}
	f := filepath.Join(dir, "store", "store.go")
	b, _ := os.ReadFile(f)
	if err := os.WriteFile(f, []byte(strings.Replace(string(b), `"id": isID,`, `"id": isOther,`, 1)+"\nfunc isOther(string) bool { return false }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code = runCLI(t, dir, "check", saved)
	if code != 1 {
		t.Errorf("check after an edit: exit %d, want 1\n%s", code, out)
	}
	wantContains(t, out, "store.isOther", "out of date")
}

func TestCheckIgnoresCommentsAndMovedLines(t *testing.T) {
	dir := sampleCopy(t)
	saved := filepath.Join(t.TempDir(), "graph.json")
	runCLI(t, dir, "build", "-o", saved)
	f := filepath.Join(dir, "store", "store.go")
	b, _ := os.ReadFile(f)
	if err := os.WriteFile(f, []byte(strings.Replace(string(b), "package store\n", "package store\n\n// A new comment that moves every line down.\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, code := runCLI(t, dir, "check", saved); code != 0 {
		t.Errorf("a comment isn't a code change: exit %d\n%s", code, out)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

// committedSample is a git repo with the sample as its first commit.
func committedSample(t *testing.T) string {
	t.Helper()
	dir := sampleCopy(t)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func editAudit(t *testing.T, dir string) {
	t.Helper()
	f := filepath.Join(dir, "store", "store.go")
	b, _ := os.ReadFile(f)
	if err := os.WriteFile(f, []byte(strings.Replace(string(b), `"INSERT INTO audit_log (id) VALUES ($1)"`, `"INSERT INTO audit_log (id, x) VALUES ($1, 1)"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAffectedFromUncommittedWork(t *testing.T) {
	dir := committedSample(t)
	editAudit(t, dir)
	out, errOut, code := runCLI(t, dir, "affected", "HEAD")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	wantContains(t, out, "(*store.DB).audit", "POST", "/orders/{id}", "1 of 4")
	if strings.Contains(out, "/health") {
		t.Errorf("/health isn't affected:\n%s", out)
	}
}

func TestAffectedAcrossCommits(t *testing.T) {
	dir := committedSample(t)
	editAudit(t, dir)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "second")
	out, _, code := runCLI(t, dir, "affected", "HEAD~1")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "(*store.DB).audit", "/orders/{id}")
}

func TestAffectedWithNoChanges(t *testing.T) {
	dir := committedSample(t)
	out, _, code := runCLI(t, dir, "affected", "HEAD")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "No code changes")
}

func TestAffectedIgnoresCommentOnlyEdits(t *testing.T) {
	dir := committedSample(t)
	f := filepath.Join(dir, "store", "store.go")
	b, _ := os.ReadFile(f)
	if err := os.WriteFile(f, []byte(strings.Replace(string(b), "func isID", "// isID checks an id.\nfunc isID", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runCLI(t, dir, "affected", "HEAD")
	wantContains(t, out, "No code changes")
}

func TestAffectedCleansUpItsWorktree(t *testing.T) {
	dir := committedSample(t)
	editAudit(t, dir)
	runCLI(t, dir, "affected", "HEAD")
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(out)), "\n"); n != 0 {
		t.Errorf("a temporary worktree was left behind:\n%s", out)
	}
	status := exec.Command("git", "status", "--short")
	status.Dir = dir
	st, _ := status.Output()
	if strings.Contains(string(st), ".repograph") {
		t.Errorf("the cache should not show up in git status:\n%s", st)
	}
}

func TestAffectedWithABadRef(t *testing.T) {
	_, errOut, code := runCLI(t, committedSample(t), "affected", "no-such-branch")
	if code != 2 || !strings.Contains(errOut, "no-such-branch") {
		t.Errorf("exit %d, %q", code, errOut)
	}
}

func TestDiffAgainstARef(t *testing.T) {
	dir := committedSample(t)
	editAudit(t, dir)
	out, _, code := runCLI(t, dir, "diff", "HEAD")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "Changed functions", "(*store.DB).audit")
}

func TestDiffBetweenSavedGraphs(t *testing.T) {
	dir := sampleCopy(t)
	before := filepath.Join(t.TempDir(), "a.json")
	after := filepath.Join(t.TempDir(), "b.json")
	runCLI(t, dir, "build", "-o", before)
	editAudit(t, dir)
	runCLI(t, dir, "build", "-o", after)
	out, _, code := runCLI(t, dir, "diff", before, after)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "(*store.DB).audit")
}

func TestDocsCommand(t *testing.T) {
	dir := sampleCopy(t)
	good := filepath.Join(dir, "good.md")
	bad := filepath.Join(dir, "bad.md")
	_ = os.WriteFile(good, []byte("`isID` in `store/store.go`\n"), 0o644)
	_ = os.WriteFile(bad, []byte("\n`isIdentifier`\n"), 0o644)
	if out, _, code := runCLI(t, dir, "docs", good); code != 0 {
		t.Errorf("good doc: exit %d\n%s", code, out)
	}
	out, _, code := runCLI(t, dir, "docs", bad)
	if code != 1 {
		t.Errorf("bad doc: exit %d, want 1", code)
	}
	wantContains(t, out, "bad.md:2", "isIdentifier")
}

func TestDiagramWriteAndCheck(t *testing.T) {
	dir := sampleCopy(t)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(dir, "README.md")
	body := "# Sample\n\n<!-- repograph:packages -->\n<!-- /repograph:packages -->\n"
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, dir, "diagram")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantContains(t, out, "```mermaid", "flowchart LR", "main --> api & store")

	if _, _, code := runCLI(t, dir, "diagram", "-check", doc); code != 1 {
		t.Errorf("an empty diagram block should fail the check: exit %d", code)
	}
	if _, _, code := runCLI(t, dir, "diagram", "-write", doc); code != 0 {
		t.Errorf("write: exit %d", code)
	}
	written, _ := os.ReadFile(doc)
	wantContains(t, string(written), "main --> api & store", "# Sample")
	if _, _, code := runCLI(t, dir, "diagram", "-check", doc); code != 0 {
		t.Errorf("a fresh diagram should pass the check: exit %d", code)
	}

	// A new import between packages makes the diagram out of date.
	f := filepath.Join(dir, "api", "extra.go")
	src := "package api\n\nimport \"example.com/sample/store\"\n\nfunc Extra() bool { return store.Validate(\"id\", \"x\") }\n"
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, code := runCLI(t, dir, "diagram", "-check", doc); code != 1 {
		t.Errorf("after a new import the check should fail: exit %d\n%s", code, out)
	}
}

func TestTagsFlag(t *testing.T) {
	dir := sampleCopy(t)
	if _, _, code := runCLI(t, dir, "callers", "Extra"); code != 2 {
		t.Errorf("Extra shouldn't exist without the tag: exit %d", code)
	}
	if _, errOut, code := runCLI(t, dir, "-tags", "extra", "callees", "Extra"); code != 0 {
		t.Errorf("with the tag: exit %d: %s", code, errOut)
	}
}
