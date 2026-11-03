// Package gitref builds a graph of the code at another git revision, in a temporary
// worktree, so a branch can be compared without saving graphs along the way.
package gitref

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vibhuthespeedst/repograph/internal/extract"
	"github.com/vibhuthespeedst/repograph/internal/graph"
)

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Build returns the graph of the module at root as it was where the current branch
// left ref, like git diff ref... does, so commits ref gained afterwards aren't
// mistaken for this branch's work. The worktree goes in a new temporary directory
// outside the repository and is removed afterwards.
func Build(root, ref string, tags []string) (*graph.Graph, []string, error) {
	sha, err := git(root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return nil, nil, fmt.Errorf("%q isn't a commit or branch in this repository", ref)
	}
	if base, err := git(root, "merge-base", "HEAD", sha); err == nil {
		sha = base
	}
	top, err := git(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, err
	}
	resolvedTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, nil, err
	}
	sub, err := filepath.Rel(resolvedTop, resolvedRoot)
	if err != nil {
		return nil, nil, err
	}

	tmp, err := os.MkdirTemp("", "repograph-")
	if err != nil {
		return nil, nil, err
	}
	tree := filepath.Join(tmp, "tree")
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err := git(root, "worktree", "add", "--quiet", "--detach", tree, sha); err != nil {
		return nil, nil, err
	}
	defer func() {
		_, _ = git(root, "worktree", "remove", "--force", tree)
		_, _ = git(root, "worktree", "prune")
	}()
	return extract.Build(filepath.Join(tree, sub), tags)
}
