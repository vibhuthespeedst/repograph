// Package docs keeps hand-written documentation honest: it reports names and paths
// that no longer exist, and generates the package diagram so it can't drift.
package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vibhuthespeedst/repograph/internal/extract"
	"github.com/vibhuthespeedst/repograph/internal/graph"
)

// Problem is one reference in a document that no longer exists.
type Problem struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Ref  string `json:"ref"`
}

var (
	tickRE  = regexp.MustCompile("`([^`\n]+)`")
	linkRE  = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	camelRE = regexp.MustCompile(`\b[a-z]+[A-Z]\w*\b`)
	wordRE  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	codeyRE = regexp.MustCompile(`[a-z0-9][A-Z]|[a-zA-Z0-9]_[a-zA-Z0-9]`)
	fileRE  = regexp.MustCompile(`\.(go|md|sql|csv|ya?ml|py|json|toml|mod|sum)$`)
)

// plain is text in backticks that isn't naming anything in the repo: commands,
// patterns, routes, flags, parameters and values.
func plain(t string) bool {
	return strings.ContainsAny(t, " \t*{}<>=@#:?&|$\"'") || strings.HasPrefix(t, "/") ||
		strings.HasPrefix(t, "-") || strings.HasPrefix(t, ".") || strings.HasPrefix(t, "_")
}

// repoIndex is what references are checked against. Tests and other docs don't
// count: a name that only survives there is still gone from the code.
type repoIndex struct {
	root  string
	files map[string]bool
	words map[string]bool
}

func indexRepo(root string) (*repoIndex, error) {
	ix := &repoIndex{root: root, files: map[string]bool{}, words: map[string]bool{}}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && extract.SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ix.files[d.Name()] = true
		if strings.HasSuffix(p, "_test.go") {
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".sql", ".csv", ".yaml", ".yml", ".json", ".toml":
			if b, err := os.ReadFile(p); err == nil {
				for _, w := range wordRE.FindAllString(string(b), -1) {
					ix.words[w] = true
				}
			}
		}
		return nil
	})
	return ix, err
}

func (ix *repoIndex) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(ix.root, filepath.FromSlash(rel)))
	return err == nil
}

// checkTick looks at one backticked token. It returns whether the token was a
// reference at all, and whether it was found.
func (ix *repoIndex) checkTick(t string) (isRef, ok bool) {
	t = strings.TrimSuffix(t, "()")
	if plain(t) {
		return false, true
	}
	if strings.Contains(t, "/") {
		// Only a path whose first part is in the repo is a claim about the repo.
		// application/json isn't.
		first, _, _ := strings.Cut(t, "/")
		if !ix.exists(first) {
			return false, true
		}
		return true, ix.exists(strings.TrimSuffix(t, "/"))
	}
	if fileRE.MatchString(t) {
		return true, ix.files[t]
	}
	name := t[strings.LastIndex(t, ".")+1:]
	if !codeyRE.MatchString(name) {
		return false, true
	}
	return true, ix.words[name]
}

// Check reports references in the given Markdown files that no longer exist. It
// returns the problems sorted by name within each file, and how many it checked.
func Check(root string, files []string) ([]Problem, int, error) {
	ix, err := indexRepo(root)
	if err != nil {
		return nil, 0, err
	}
	var problems []Problem
	checked := 0
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, 0, err
		}
		var found []Problem
		seen := map[string]bool{}
		report := func(line int, ref string) {
			if !seen[ref] {
				seen[ref] = true
				found = append(found, Problem{File: file, Line: line, Ref: ref})
			}
		}
		fence := ""
		for i, line := range strings.Split(string(b), "\n") {
			n := i + 1
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") {
				if fence == "" {
					fence = strings.TrimPrefix(trimmed, "```")
					if fence == "" {
						fence = "code"
					}
				} else {
					fence = ""
				}
				continue
			}
			if fence == "mermaid" {
				for _, name := range camelRE.FindAllString(line, -1) {
					checked++
					if !ix.words[name] {
						report(n, name)
					}
				}
				continue
			}
			if fence != "" {
				continue
			}
			for _, m := range tickRE.FindAllStringSubmatch(line, -1) {
				if isRef, ok := ix.checkTick(m[1]); isRef {
					checked++
					if !ok {
						report(n, strings.TrimSuffix(m[1], "()"))
					}
				}
			}
			for _, m := range linkRE.FindAllStringSubmatch(line, -1) {
				target, _, _ := strings.Cut(m[1], "#")
				if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				checked++
				if _, err := os.Stat(filepath.Join(filepath.Dir(file), filepath.FromSlash(target))); err != nil {
					report(n, m[1])
				}
			}
		}
		sort.Slice(found, func(a, b int) bool { return found[a].Ref < found[b].Ref })
		problems = append(problems, found...)
	}
	return problems, checked, nil
}

var (
	nonWordRE = regexp.MustCompile(`[^A-Za-z0-9_]`)
	// Words Mermaid reads as its own syntax can't be used as node IDs.
	reserved = map[string]bool{"end": true, "graph": true, "flowchart": true, "subgraph": true, "class": true, "style": true, "click": true, "default": true}
)

func node(pkg string) string {
	label := strings.TrimPrefix(pkg, "internal/")
	id := nonWordRE.ReplaceAllString(label, "_")
	if reserved[id] {
		id += "_"
	}
	if id == label {
		return id
	}
	return fmt.Sprintf("%s[%q]", id, label)
}

// PackageDiagram draws which packages import which, as a Mermaid block. Commands
// are left out when anything else remains, because they import most packages to
// wire them up, and those extra arrows make the rest unreadable.
func PackageDiagram(g *graph.Graph) string {
	isCommand := func(p string) bool { return p == "main" || strings.HasPrefix(p, "cmd/") }
	sources := make([]string, 0, len(g.Imports))
	for pkg := range g.Imports {
		if !isCommand(pkg) {
			sources = append(sources, pkg)
		}
	}
	if len(sources) == 0 {
		for pkg := range g.Imports {
			sources = append(sources, pkg)
		}
	}
	sort.Strings(sources)
	var b strings.Builder
	b.WriteString("```mermaid\nflowchart LR\n")
	for _, pkg := range sources {
		targets := make([]string, 0, len(g.Imports[pkg]))
		for _, t := range g.Imports[pkg] {
			targets = append(targets, node(t))
		}
		fmt.Fprintf(&b, "    %s --> %s\n", node(pkg), strings.Join(targets, " & "))
	}
	b.WriteString("```")
	return b.String()
}

const (
	startMarker = "<!-- repograph:packages -->"
	endMarker   = "<!-- /repograph:packages -->"
)

// ReplaceDiagram puts the diagram between the markers in a Markdown document. Markers
// inside a code block don't count, so a doc can show them as an example.
func ReplaceDiagram(doc, diagram string) (string, error) {
	start, end, offset := -1, -1, 0
	inFence := false
	for _, line := range strings.SplitAfter(doc, "\n") {
		switch trimmed := strings.TrimSpace(line); {
		case strings.HasPrefix(trimmed, "```"):
			inFence = !inFence
		case !inFence && trimmed == startMarker && start < 0:
			start = offset
		case !inFence && trimmed == endMarker && start >= 0 && end < 0:
			end = offset
		}
		offset += len(line)
	}
	if start < 0 || end < 0 {
		return "", fmt.Errorf("add these two lines where the diagram should go:\n%s\n%s", startMarker, endMarker)
	}
	return doc[:start] + startMarker + "\n" + diagram + "\n" + doc[end:], nil
}
