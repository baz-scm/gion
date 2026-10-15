package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/baz-scm/gion/internal/repo"
)

func tag(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	msg := fs.String("m", "", "message; makes an annotated tag (recommended for releases)")
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 || len(pos) > 3 {
		return fmt.Errorf("usage: %s tag <remote> <name> [<rev>] [-m <msg>]", prog)
	}
	rev := "HEAD"
	if len(pos) == 3 {
		rev = pos[2]
	}
	var who repo.People
	if *msg != "" {
		if who, err = repo.Identity(); err != nil {
			return err
		}
	}
	r, err := repo.Open(pos[0])
	if err != nil {
		return err
	}
	defer r.Close()
	res, err := r.Tag(ctx, repo.TagOptions{Name: pos[1], Rev: rev, Message: *msg, Who: who})
	if err != nil {
		return err
	}
	kind := "lightweight"
	if res.Object != res.Commit {
		kind = "annotated"
	}
	if *md {
		fmt.Printf("## Tagged `%s`\n\n- %s tag on commit `%s` (%s)\n", res.Ref.Short(), kind, res.Commit, rev)
		return nil
	}
	fmt.Printf("%s\t%s\n", res.Object, res.Ref)
	return nil
}
