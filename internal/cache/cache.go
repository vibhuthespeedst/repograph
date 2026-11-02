// Package cache keeps a built graph next to the code and rebuilds it only when the
// code has changed, so every question is answered from current source with no
// rebuild step to remember.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vibhuthespeedst/repograph/internal/extract"
	"github.com/vibhuthespeedst/repograph/internal/graph"
)

// Dir is the cache directory inside a repository. It ignores itself in git.
const Dir = ".repograph"

// Result is a graph and how it was obtained.
type Result struct {
	Graph    *graph.Graph
	Rebuilt  bool
	Warnings []string
}

// FindRoot walks up from dir to the nearest go.mod.
func FindRoot(dir string) (string, error) {
	cur, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no go.mod found in %s or above it; run this inside a Go module, or pass -C <dir>", dir)
		}
		cur = parent
	}
}

// fingerprint covers everything the graph is built from: the Go and SQL files, the
// module files, the tags and the format. Content is hashed instead of trusting file
// times, so switching branches can't leave a stale graph looking current.
func fingerprint(root string, tags []string) (string, error) {
	var files []string
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
		switch n := d.Name(); {
		case strings.HasSuffix(n, ".go"), strings.HasSuffix(n, ".sql"), n == "go.mod", n == "go.sum":
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	fmt.Fprintf(h, "format %d\ntags %s\n", graph.FormatVersion, strings.Join(tags, ","))
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%s\n", filepath.ToSlash(rel))
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Ensure creates the cache directory, set to ignore itself.
func Ensure(root string) (string, error) {
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Load returns the graph for the module at root, rebuilding it if the code changed.
// Failing to write the cache doesn't fail the question, and a partial graph from
// broken code isn't cached, so its warnings show on every run until the code is fixed.
func Load(root string, tags []string) (Result, error) {
	want, err := fingerprint(root, tags)
	if err != nil {
		return Result{}, err
	}
	graphPath := filepath.Join(root, Dir, "graph.json")
	stampPath := filepath.Join(root, Dir, "fingerprint")
	if got, err := os.ReadFile(stampPath); err == nil && string(got) == want {
		if g, err := graph.Load(graphPath); err == nil {
			return Result{Graph: g}, nil
		}
	}

	g, warnings, err := extract.Build(root, tags)
	if err != nil {
		return Result{Warnings: warnings}, err
	}
	if len(warnings) == 0 {
		if _, err := Ensure(root); err == nil {
			if err := g.Save(graphPath); err == nil {
				_ = os.WriteFile(stampPath, []byte(want), 0o644)
			}
		}
	}
	return Result{Graph: g, Rebuilt: true, Warnings: warnings}, nil
}
