// Package cache keeps a built graph and rebuilds it only when the code has changed,
// so every question is answered from current source with no rebuild step to remember.
//
// The cache lives in the user cache directory, never inside the repository. A cloned
// repo can commit any file, including a fake cache or a link pointing outside the
// repo, so nothing in the repo is read or written as cache.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/vibhuthespeedst/repograph/internal/extract"
	"github.com/vibhuthespeedst/repograph/internal/graph"
)

// EnvDir names an environment variable that, when set, replaces the user cache
// directory as the place caches are kept.
const EnvDir = "REPOGRAPH_CACHE_DIR"

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

// paths returns where the graph and its fingerprint are kept for the module at root:
// a directory per module, named by a hash of its location.
func paths(root string) (graphPath, stampPath string, err error) {
	base := os.Getenv(EnvDir)
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", "", err
		}
		base = filepath.Join(userCache, "repograph")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs) // E:\x and e:\x are the same folder
	}
	sum := sha256.Sum256([]byte(abs))
	dir := filepath.Join(base, hex.EncodeToString(sum[:8]))
	return filepath.Join(dir, "graph.json"), filepath.Join(dir, "fingerprint"), nil
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

// save writes the graph and then its fingerprint, each to a temporary file renamed
// into place, so a crash or a second run can't leave a fingerprint naming a graph
// that was never fully written.
func save(g *graph.Graph, graphPath, stampPath, stamp string) error {
	if err := os.MkdirAll(filepath.Dir(graphPath), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", graphPath, os.Getpid())
	if err := g.Save(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, graphPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	tmp = fmt.Sprintf("%s.%d.tmp", stampPath, os.Getpid())
	if err := os.WriteFile(tmp, []byte(stamp), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, stampPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Load returns the graph for the module at root, rebuilding it if the code changed.
// Failing to read or write the cache doesn't fail the question, and a partial graph
// from broken code isn't cached, so its warnings show on every run until the code
// is fixed.
func Load(root string, tags []string) (Result, error) {
	want, err := fingerprint(root, tags)
	if err != nil {
		return Result{}, err
	}
	graphPath, stampPath, pathErr := paths(root)
	if pathErr == nil {
		if got, err := os.ReadFile(stampPath); err == nil && string(got) == want {
			if g, err := graph.Load(graphPath); err == nil {
				return Result{Graph: g}, nil
			}
		}
	}

	g, warnings, err := extract.Build(root, tags)
	if err != nil {
		return Result{Warnings: warnings}, err
	}
	if len(warnings) == 0 && pathErr == nil {
		_ = save(g, graphPath, stampPath, want)
	}
	return Result{Graph: g, Rebuilt: true, Warnings: warnings}, nil
}
