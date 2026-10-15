// Command any is a thin git client: it holds only what it needs and pulls
// files, commits and diffs from the remote on demand.
package main

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

	"github.com/AnyGitForge/AnyForge/cli/internal/repo"
	"github.com/AnyGitForge/AnyForge/cli/internal/wire"
)

const usage = `usage: any <command> <remote> [args]

commands:
  ls-refs <remote> [--prefix p]...     list remote refs (protocol v2)
  cat     <remote> <rev>:<path>        print a file's raw bytes
  ls      <remote> [<rev>[:<dir>]]     list a directory (default HEAD:)
  commit  <remote> -m <msg> <path>=<local>... [--rm <path>]...
          [--branch <name> [--onto <rev>] | --to <branch>]
          build a commit from the changes and push it; no checkout needed.
          default: new branch any/<timestamp> from HEAD.
          --to: update an existing branch (compare-and-swap, retries on
          upstream moves that don't touch your paths).

<remote> is a URL or GitHub "owner/repo".
flags: --md  markdown output for agents
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmds := map[string]func(context.Context, []string) error{
		"ls-refs": lsRefs,
		"cat":     cat,
		"ls":      ls,
		"commit":  commit,
	}
	name := os.Args[1]
	if name == "-h" || name == "--help" || name == "help" {
		fmt.Print(usage)
		return
	}
	fn, ok := cmds[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "any: unknown command %q\n\n%s", name, usage)
		os.Exit(2)
	}
	if err := fn(ctx, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "any:", err)
		os.Exit(1)
	}
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
		return errors.New("usage: any ls-refs <remote> [--prefix p]...")
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
		return errors.New("usage: any cat <remote> <rev>:<path>")
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
		return fmt.Errorf("%s is a directory; use `any ls`", p)
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
		return errors.New("usage: any ls <remote> [<rev>[:<dir>]]")
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
		return fmt.Errorf("%s is a file; use `any cat`", p)
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
	branch := fs.String("branch", "", "new branch to create (default any/<timestamp>)")
	to := fs.String("to", "", "existing branch to update with compare-and-swap")
	md := fs.Bool("md", false, "markdown output")
	var rms multiFlag
	fs.Var(&rms, "rm", "path to delete (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || *msg == "" {
		return errors.New("usage: any commit <remote> -m <msg> <path>=<local>... [--rm <path>]...")
	}
	if *to != "" && *branch != "" {
		return errors.New("--to and --branch are exclusive")
	}
	if *to == "" && *branch == "" {
		*branch = "any/" + time.Now().UTC().Format("20060102-150405")
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

	author, err := repo.Identity()
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
		Changes: changes, Message: *msg, Author: author,
		Onto: *onto, Branch: *branch, To: *to,
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
