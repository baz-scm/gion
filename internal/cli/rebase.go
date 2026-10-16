package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/workspace"
)

func rebase(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rebase", flag.ContinueOnError)
	cont := fs.Bool("continue", false, "finish the stopped commit with your resolved files")
	skip := fs.Bool("skip", false, "drop the stopped commit")
	abort := fs.Bool("abort", false, "put everything back as before the rebase")
	onto := fs.String("onto", "", "remote mode: rev to rebase onto (default: the default branch)")
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 2 {
		return remoteRebase(ctx, pos[0], pos[1], *onto, *md)
	}
	n := 0
	for _, b := range []bool{*cont, *skip, *abort} {
		if b {
			n++
		}
	}
	if n > 1 || len(pos) > 1 || (n == 1 && len(pos) > 0) {
		return fmt.Errorf("usage: %s rebase [<onto>] | --continue | --skip | --abort  (in a workspace)\n       %s rebase <remote> <branch> [--onto <rev>]", prog, prog)
	}
	w, err := workspace.Find(".")
	if err != nil {
		return fmt.Errorf("%w\n(or `%s rebase <remote> <branch>` without a workspace)", err, prog)
	}
	r, err := repo.Open(w.State.Remote)
	if err != nil {
		return err
	}
	defer r.Close()

	var res workspace.RebaseResult
	switch {
	case *abort:
		if err := w.RebaseAbort(ctx, r); err != nil {
			return err
		}
		if *md {
			fmt.Println("Rebase aborted. Files are back as before it; nothing was pushed.")
		} else {
			fmt.Println("rebase aborted; nothing was pushed")
		}
		return nil
	case *cont:
		res, err = w.RebaseContinue(ctx, r)
	case *skip:
		res, err = w.RebaseSkip(ctx, r)
	default:
		target := ""
		if len(pos) == 1 {
			target = pos[0]
		}
		res, err = w.Rebase(ctx, r, target)
	}
	if err != nil {
		return err
	}
	printRebase(res, *md)
	return nil
}

func printRebase(res workspace.RebaseResult, md bool) {
	printRebaseIn(res, md, true)
}

func printRebaseIn(res workspace.RebaseResult, md, inWorkspace bool) {
	switch {
	case res.UpToDate:
		if md {
			fmt.Printf("`%s` already contains `%s`; nothing to do.\n", res.Branch, res.Onto)
		} else {
			fmt.Printf("%s already contains %s\n", res.Branch, res.Onto)
		}
	case res.Stopped != nil:
		c := res.Stopped.Commit
		if md {
			fmt.Printf("## Rebase of `%s` onto `%s` stopped on a conflict\n\n", res.Branch, res.Onto)
			fmt.Printf("- Commit `%s` %s (%d more after it; %d replayed so far)\n\n", c.Hash.String()[:12], subject(c), res.Remaining, res.Picked)
			fmt.Println("| file | conflict |\n| --- | --- |")
			for _, cf := range res.Stopped.Conflicts {
				fmt.Printf("| `%s` | %s |\n", cf.Path, conflictHelp(cf.Kind, res.Onto))
			}
			fmt.Printf("\nThe files are in the workspace. Content conflicts have markers: `<<<<<<< %s` (the new base), `||||||| base` (before either change), `=======`, then `>>>>>>>` (this commit). Edit each file to its final content, or delete it to delete it, then run `%s rebase --continue`. `--skip` drops this commit; `--abort` restores everything. Nothing is pushed until the rebase finishes.\n", res.Onto, prog)
			return
		}
		fmt.Printf("conflict replaying %s %s (%d more after it):\n", c.Hash.String()[:12], subject(c), res.Remaining)
		for _, cf := range res.Stopped.Conflicts {
			fmt.Printf("  %s\t%s\n", cf.Kind, cf.Path)
		}
		fmt.Printf("resolve the files, then `%s rebase --continue` (or --skip / --abort)\n", prog)
	default:
		n := len(res.Updated.Updated) + len(res.Updated.Removed)
		if md {
			fmt.Printf("## Rebased `%s` onto `%s`\n\n", res.Branch, res.Onto)
			fmt.Printf("- `%s` → `%s`: %d commit(s) replayed, %d skipped (already in `%s`)\n", res.OldTip.String()[:12], res.NewTip.String()[:12], res.Picked, res.Skipped, res.Onto)
			if inWorkspace {
				fmt.Printf("- Pushed with compare-and-swap; %d file(s) in the workspace updated.\n", n)
			} else {
				fmt.Println("- Pushed with compare-and-swap.")
			}
			return
		}
		fmt.Printf("rebased %s onto %s: %s -> %s (%d replayed, %d skipped, %d files updated)\n", res.Branch, res.Onto, res.OldTip.String()[:12], res.NewTip.String()[:12], res.Picked, res.Skipped, n)
	}
}

func conflictHelp(kind, onto string) string {
	switch kind {
	case "modify/delete":
		return fmt.Sprintf("this commit deletes it, `%s` changed it (on disk: `%s`'s version; delete it to accept the deletion)", onto, onto)
	case "delete/modify":
		return fmt.Sprintf("`%s` deleted it, this commit changes it (on disk: this commit's version; delete it to keep it deleted)", onto)
	case "binary":
		return fmt.Sprintf("binary file changed on both sides (on disk: `%s`'s version)", onto)
	}
	return "both changed the same lines (markers in the file)"
}

// remoteRebase rebases a branch with no workspace. It only succeeds without
// conflicts; for those it points to a workspace.
func remoteRebase(ctx context.Context, remote, branch, onto string, md bool) error {
	r, err := repo.Open(remote)
	if err != nil {
		return err
	}
	defer r.Close()
	if onto == "" {
		if onto, err = r.DefaultBranch(ctx); err != nil {
			return err
		}
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	st, err := r.StartRebase(ctx, branch, onto)
	if errors.Is(err, repo.ErrUpToDate) {
		printRebase(workspace.RebaseResult{Branch: branch, Onto: onto, UpToDate: true}, md)
		return nil
	}
	if err != nil {
		return err
	}
	who, err := repo.Identity()
	if err != nil {
		return err
	}
	stopped, err := r.Replay(ctx, st, who.Committer)
	if err != nil {
		return err
	}
	if stopped != nil {
		var paths []string
		for _, cf := range stopped.Conflicts {
			paths = append(paths, cf.Path)
		}
		return fmt.Errorf("%s conflicts with %s in %s; nothing was pushed. Resolve it in a workspace:\n  %s clone %s ws --path %s && cd ws && %s switch %s && %s rebase %s",
			short12(stopped.Commit.Hash), onto, strings.Join(paths, ", "), prog, remote, strings.Join(paths, " --path "), prog, branch, prog, onto)
	}
	if err := r.PushRebase(ctx, st); err != nil {
		return err
	}
	printRebaseIn(workspace.RebaseResult{Branch: branch, Onto: onto, OldTip: plumbing.NewHash(st.OrigTip), NewTip: plumbing.NewHash(st.NewTip), Picked: st.Picked, Skipped: st.Skipped}, md, false)
	return nil
}

func short12(h plumbing.Hash) string { return h.String()[:12] }
