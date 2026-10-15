// Package cli implements the gion command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/wire"
	"github.com/baz-scm/gion/internal/workspace"
)

const usageText = `usage: %s <command> [args]

commands:
  ls-refs <remote> [--prefix p]...     list remote refs (protocol v2)
  cat     <remote> <rev>:<path>        print a file's raw bytes
  ls      <remote> [<rev>[:<dir>]]     list a directory (default HEAD:)
  find    <remote> [<rev>] <pattern>  search file paths across the whole repo
                                       (substring, or glob with * ? ** [])
  grep    <remote> [<rev>] <regex> [--path p]... [-i] [-l] [--local|--forge]
          search file contents: scans locally when the candidate files are
          few (--max-files, default 5000), else asks GitHub code search for
          candidates and verifies them locally
  clone   <remote> <dir> [--ref r] [--path p]...
          light workspace: only the chosen paths (default all) at one
          commit, no history, no .git. Then, inside it:
  status                               changed files vs. the clone
                                       (honors .gitignore)
  sync                                 pull upstream changes into files you
                                       haven't edited; stops on conflicts
  commit  -m <msg> [--branch b]        push workspace changes; first commit
                                       creates a branch, later ones update it
  commit  <remote> -m <msg> <path>=<local>... [--rm <path>]...
          [--branch <name> [--onto <rev> | --orphan] | --to <branch>]
          build a commit from the changes and push it; no checkout needed.
          default: new branch gion/<timestamp> from HEAD.
          --to: update an existing branch (compare-and-swap, retries on
          upstream moves that don't touch your paths).

  tag     <remote> <name> [<rev>] [-m <msg>]
          create a tag on the remote (annotated with -m); never moves one
  version                              print the gion version

<remote> is a URL or GitHub "owner/repo".
flags: --md  markdown output for agents
`

// prog is the command name used in help and error text.
var prog = "gion"

// Main runs the CLI as prog and returns the process exit code.
func Main(invokedAs string, args []string) int {
	prog = invokedAs
	usage := fmt.Sprintf(usageText, prog)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	cmds := map[string]func(context.Context, []string) error{
		"ls-refs": lsRefs,
		"cat":     cat,
		"ls":      ls,
		"commit":  commit,
		"clone":   clone,
		"status":  status,
		"sync":    sync,
		"version": versionCmd,
		"find":    find,
		"grep":    grep,
		"tag":     tag,
	}
	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		fmt.Print(usage)
		return 0
	}
	fn, ok := cmds[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown command %q\n\n%s", prog, name, usage)
		return 2
	}
	if err := fn(ctx, args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", prog, err)
		return 1
	}
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// parse lets flags and positionals mix in any order.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %w", fs.Name(), err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func lsRefs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls-refs", flag.ContinueOnError)
	var prefixes multiFlag
	fs.Var(&prefixes, "prefix", "ref prefix to list (repeatable; default HEAD, refs/heads/)")
	timing := fs.Bool("time", false, "print elapsed time to stderr")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: %s ls-refs <remote> [--prefix p]...", prog)
	}
	if len(prefixes) == 0 {
		prefixes = wire.DefaultPrefixes
	}

	start := time.Now()
	sess, err := wire.Open(ctx, pos[0])
	if err != nil {
		return err
	}
	defer sess.Close()
	refs, err := sess.LsRefs(ctx, prefixes)
	if err != nil {
		return err
	}
	// Same shape as `git ls-remote --symref`.
	for _, r := range refs {
		if r.Target != "" {
			fmt.Printf("ref: %s\t%s\n", r.Target, r.Name)
		}
		if r.OID.IsZero() {
			continue // unborn
		}
		fmt.Printf("%s\t%s\n", r.OID, r.Name)
		if !r.Peeled.IsZero() {
			fmt.Printf("%s\t%s^{}\n", r.Peeled, r.Name)
		}
	}
	if *timing {
		fmt.Fprintf(os.Stderr, "%d refs in %s\n", len(refs), time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// cat always prints raw bytes: wrapping file contents would corrupt them.
func cat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cat", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || !strings.Contains(pos[1], ":") {
		return fmt.Errorf("usage: %s cat <remote> <rev>:<path>", prog)
	}
	r, err := repo.Open(pos[0])
	if err != nil {
		return err
	}
	defer r.Close()

	rev, p := repo.SplitSpec(pos[1])
	_, root, err := r.RootTree(ctx, rev)
	if err != nil {
		return err
	}
	e, err := r.Lookup(ctx, root, p)
	if err != nil {
		return err
	}
	if !e.Mode.IsFile() {
		return fmt.Errorf("%s is a directory; use `%s ls`", p, prog)
	}
	data, err := r.Blob(ctx, e.Hash)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func ls(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return fmt.Errorf("usage: %s ls <remote> [<rev>[:<dir>]]", prog)
	}
	spec := "HEAD"
	if len(pos) == 2 {
		spec = pos[1]
	}
	r, err := repo.Open(pos[0])
	if err != nil {
		return err
	}
	defer r.Close()

	rev, p := repo.SplitSpec(spec)
	commit, root, err := r.RootTree(ctx, rev)
	if err != nil {
		return err
	}
	e, err := r.Lookup(ctx, root, p)
	if err != nil {
		return err
	}
	if e.Mode.IsFile() {
		return fmt.Errorf("%s is a file; use `%s cat`", p, prog)
	}
	t, err := r.Tree(ctx, e.Hash)
	if err != nil {
		return err
	}

	if *md {
		dir := "/" + strings.Trim(p, "/")
		fmt.Printf("## `%s` @ `%s` (`%s`)\n\n", dir, rev, commit.String()[:12])
		fmt.Println("| type | name | oid |")
		fmt.Println("| --- | --- | --- |")
		for _, en := range t.Entries {
			kind, name := "file", en.Name
			if !en.Mode.IsFile() {
				kind, name = "dir", en.Name+"/"
			}
			fmt.Printf("| %s | `%s` | `%s` |\n", kind, name, en.Hash.String()[:12])
		}
		fmt.Printf("\n%d entries.\n", len(t.Entries))
		return nil
	}
	// Same shape as `git ls-tree`.
	for _, en := range t.Entries {
		kind := "blob"
		if !en.Mode.IsFile() {
			kind = "tree"
		}
		fmt.Printf("%06o %s %s\t%s\n", uint32(en.Mode), kind, en.Hash, en.Name)
	}
	return nil
}

func commit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	msg := fs.String("m", "", "commit message (required)")
	onto := fs.String("onto", "HEAD", "base rev for a new branch")
	branch := fs.String("branch", "", "new branch to create (default gion/<timestamp>)")
	to := fs.String("to", "", "existing branch to update with compare-and-swap")
	orphan := fs.Bool("orphan", false, "make a root commit with no parent (e.g. in an empty repo)")
	md := fs.Bool("md", false, "markdown output")
	var rms multiFlag
	fs.Var(&rms, "rm", "path to delete (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *msg == "" {
		return fmt.Errorf("usage: %s commit [<remote>] -m <msg> [<path>=<local>...] [--rm <path>]...", prog)
	}
	if len(pos) == 0 {
		if *to != "" || len(rms) > 0 || *onto != "HEAD" || *orphan {
			return errors.New("--to, --rm, --onto and --orphan are for remote mode; in a workspace just edit files")
		}
		return workspaceCommit(ctx, *msg, *branch, *md)
	}
	if *to != "" && *branch != "" {
		return errors.New("--to and --branch are exclusive")
	}
	if *orphan && (*to != "" || *onto != "HEAD") {
		return errors.New("--orphan creates a new branch; it can't be combined with --to or --onto")
	}
	if *to == "" && *branch == "" {
		*branch = "gion/" + time.Now().UTC().Format("20060102-150405")
	}

	var changes []repo.Change
	for _, spec := range pos[1:] {
		dst, src, ok := strings.Cut(spec, "=")
		if !ok || dst == "" || src == "" {
			return fmt.Errorf("bad change %q: want <path>=<local file>", spec)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		changes = append(changes, repo.Change{Path: dst, Data: data})
	}
	for _, p := range rms {
		changes = append(changes, repo.Change{Path: p, Delete: true})
	}

	who, err := repo.Identity()
	if err != nil {
		return err
	}
	r, err := repo.Open(pos[0])
	if err != nil {
		return err
	}
	defer r.Close()

	start := time.Now()
	res, err := r.CommitAndPush(ctx, repo.CommitOptions{
		Changes: changes, Message: *msg, Who: who,
		Onto: *onto, Branch: *branch, To: *to, Orphan: *orphan,
	})
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Round(time.Millisecond)

	branchName := res.Ref.Short()
	if *md {
		verb := "Updated"
		if res.Created {
			verb = "Created"
		}
		fmt.Printf("## Pushed `%s`\n\n", res.Commit.String()[:12])
		fmt.Printf("- %s branch `%s`\n", verb, branchName)
		fmt.Printf("- Commit: `%s`\n- Parent: `%s`\n", res.Commit, res.Parent)
		fmt.Printf("- %d changed paths, %d objects sent, %d attempt(s), %s\n", len(changes), res.Objects, res.Attempts, elapsed)
		if u := compareURL(pos[0], branchName); u != "" && res.Created {
			fmt.Printf("- Open a PR: %s\n", u)
		}
		return nil
	}
	fmt.Printf("%s\t%s\n", res.Commit, res.Ref)
	return nil
}

// compareURL returns GitHub's "open a PR" link for owner/repo remotes.
func compareURL(remote, branch string) string {
	u := wire.RemoteURL(remote)
	const gh = "https://github.com/"
	if !strings.HasPrefix(u, gh) {
		return ""
	}
	return strings.TrimSuffix(u, ".git") + "/compare/" + branch + "?expand=1"
}

func clone(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	ref := fs.String("ref", "HEAD", "rev to clone")
	md := fs.Bool("md", false, "markdown output")
	var paths multiFlag
	fs.Var(&paths, "path", "path to include (repeatable; default whole repo)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return fmt.Errorf("usage: %s clone <remote> <dir> [--ref r] [--path p]...", prog)
	}
	r, err := repo.Open(pos[0])
	if err != nil {
		return err
	}
	defer r.Close()

	start := time.Now()
	_, res, err := workspace.Clone(ctx, r, pos[1], *ref, paths)
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	scope := "whole repo"
	if len(paths) > 0 {
		scope = strings.Join(paths, ", ")
	}
	if *md {
		fmt.Printf("## Cloned `%s` into `%s`\n\n", pos[0], pos[1])
		fmt.Printf("- Commit: `%s` (%s)\n- Scope: %s\n", res.Commit, *ref, scope)
		fmt.Printf("- %d files, %s, %s\n", res.Files, humanBytes(res.Bytes), elapsed)
		fmt.Printf("- No history and no `.git`: use `%s status` and `%s commit` inside it.\n", prog, prog)
		return nil
	}
	fmt.Printf("cloned %s@%s into %s: %d files, %s (%s) in %s\n", pos[0], res.Commit.String()[:12], pos[1], res.Files, humanBytes(res.Bytes), scope, elapsed)
	return nil
}

func status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	w, err := workspace.Find(".")
	if err != nil {
		return err
	}
	changes, err := w.Status()
	if err != nil {
		return err
	}
	st := w.State
	target := st.Branch
	if target == "" {
		target = "(new branch on first commit)"
	}
	if *md {
		fmt.Printf("## Workspace `%s`\n\n", st.Remote)
		fmt.Printf("- Base: `%s` (cloned from `%s`)\n- Pushes to: `%s`\n\n", st.Base[:12], st.Ref, target)
		if len(changes) == 0 {
			fmt.Println("No changes.")
			return nil
		}
		fmt.Println("| change | path |")
		fmt.Println("| --- | --- |")
		for _, c := range changes {
			fmt.Printf("| %s | `%s` |\n", c.Kind, c.Path)
		}
		return nil
	}
	fmt.Printf("on %s @ %s -> %s\n", st.Ref, st.Base[:12], target)
	for _, c := range changes {
		fmt.Printf("%s\t%s\n", map[workspace.Kind]string{workspace.Added: "A", workspace.Modified: "M", workspace.Deleted: "D"}[c.Kind], c.Path)
	}
	return nil
}

func workspaceCommit(ctx context.Context, msg, branch string, md bool) error {
	w, err := workspace.Find(".")
	if err != nil {
		return err
	}
	r, err := repo.Open(w.State.Remote)
	if err != nil {
		return err
	}
	defer r.Close()

	start := time.Now()
	res, err := w.Commit(ctx, r, msg, branch)
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	branchName := res.Ref.Short()
	if md {
		verb := "Updated"
		if res.Created {
			verb = "Created"
		}
		fmt.Printf("## Pushed `%s`\n\n", res.Commit.String()[:12])
		fmt.Printf("- %s branch `%s`\n- Commit: `%s`\n- Parent: `%s`\n", verb, branchName, res.Commit, res.Parent)
		fmt.Printf("- %d changed paths, %d objects sent, %d attempt(s), %s\n", len(res.Changes), res.Objects, res.Attempts, elapsed)
		if n := len(res.Pulled.Updated) + len(res.Pulled.Removed); n > 0 {
			fmt.Printf("- `%s` had upstream commits on other paths; updated %d file(s) on disk to match.\n", branchName, n)
		}
		if u := compareURL(w.State.Remote, branchName); u != "" && res.Created {
			fmt.Printf("- Open a PR: %s\n", u)
		}
		return nil
	}
	fmt.Printf("%s\t%s\n", res.Commit, res.Ref)
	if n := len(res.Pulled.Updated) + len(res.Pulled.Removed); n > 0 {
		fmt.Fprintf(os.Stderr, "note: %s had upstream commits on other paths; updated %d file(s) on disk\n", branchName, n)
	}
	return nil
}

func sync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	w, err := workspace.Find(".")
	if err != nil {
		return err
	}
	r, err := repo.Open(w.State.Remote)
	if err != nil {
		return err
	}
	defer r.Close()

	res, err := w.Sync(ctx, r)
	if err != nil {
		if errors.Is(err, workspace.ErrLocalConflict) {
			return fmt.Errorf("%w\nnothing was changed; commit or undo those edits, then sync again", err)
		}
		return err
	}
	if res.From == res.To {
		if *md {
			fmt.Printf("Already up to date at `%s`.\n", res.To.String()[:12])
		} else {
			fmt.Printf("up to date at %s\n", res.To.String()[:12])
		}
		return nil
	}
	if *md {
		fmt.Printf("## Synced `%s` → `%s`\n\n", res.From.String()[:12], res.To.String()[:12])
		if len(res.Updated)+len(res.Removed) == 0 {
			fmt.Println("No changes inside this workspace's paths.")
		}
		for _, p := range res.Updated {
			fmt.Printf("- updated `%s`\n", p)
		}
		for _, p := range res.Removed {
			fmt.Printf("- removed `%s`\n", p)
		}
		return nil
	}
	fmt.Printf("%s -> %s\n", res.From.String()[:12], res.To.String()[:12])
	for _, p := range res.Updated {
		fmt.Printf("U\t%s\n", p)
	}
	for _, p := range res.Removed {
		fmt.Printf("D\t%s\n", p)
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
