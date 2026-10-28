package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/forge"
	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/workspace"
)

func branch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("branch", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: %s branch rename [<remote> <old>] <new>  (in a workspace, <old> defaults to its branch)", prog)
	if len(pos) < 2 || pos[0] != "rename" {
		return usage
	}
	pos = pos[1:]

	var w *workspace.Workspace
	var remote, from, to string
	switch len(pos) {
	case 3:
		remote, from, to = pos[0], pos[1], pos[2]
	case 1, 2:
		if w, err = workspace.Find("."); err != nil {
			return fmt.Errorf("%w\n%v", err, usage)
		}
		remote, to = w.State.Remote, pos[len(pos)-1]
		if len(pos) == 2 {
			from = pos[0]
		} else if from = w.State.Branch; from == "" {
			return errors.New("this workspace has no branch yet (its first commit creates one); name the branch to rename")
		}
	default:
		return usage
	}

	from, to = strings.TrimPrefix(from, "refs/heads/"), strings.TrimPrefix(to, "refs/heads/")

	r, err := repo.Open(remote)
	if err != nil {
		return err
	}
	defer r.Close()
	tip, err := renameBranch(ctx, r, remote, from, to)
	if err != nil {
		return err
	}
	followed := false
	if w != nil {
		if followed, err = w.BranchRenamed(from, to); err != nil {
			return fmt.Errorf("renamed %s to %s, but updating the workspace failed: %w", from, to, err)
		}
	}

	if *md {
		fmt.Printf("## Renamed `%s` → `%s`\n\n- Tip: `%s`\n", from, to, tip)
		if followed {
			fmt.Printf("- This workspace now follows `%s`.\n", to)
		}
		return nil
	}
	fmt.Printf("renamed %s -> %s @ %s\n", from, to, tip.String()[:12])
	return nil
}

// renameBranch renames from to to on the remote. GitHub's API keeps open
// pull requests on the branch; elsewhere it's one atomic push.
func renameBranch(ctx context.Context, r *repo.Repo, remote, from, to string) (plumbing.Hash, error) {
	if _, ok := forge.GitHubRepo(remote); !ok {
		return r.RenameBranch(ctx, from, to)
	}
	if err := plumbing.NewBranchReferenceName(to).Validate(); err != nil || to == "" || to == from {
		return plumbing.ZeroHash, fmt.Errorf("invalid new branch name %q", to)
	}
	if err := forge.RenameBranch(ctx, remote, from, to); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.Resolve(ctx, "refs/heads/"+to)
}
