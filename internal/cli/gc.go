package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/baz-scm/gion/internal/cache"
)

func gc(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	maxFlag := fs.String("max", "", "size cap for this run (default $GION_CACHE_MAX or 1GB)")
	md := fs.Bool("md", false, "markdown output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	max, err := cache.MaxSize()
	if err != nil {
		return err
	}
	if *maxFlag != "" {
		if max, err = cache.ParseSize(*maxFlag); err != nil {
			return err
		}
	}
	root, err := cache.Root()
	if err != nil {
		return err
	}
	st, err := cache.Evict(root, max, time.Now())
	if err != nil {
		return err
	}
	limit := "no limit"
	if max > 0 {
		limit = humanBytes(max)
	}
	if *md {
		fmt.Printf("## Cache `%s`\n\n- Size: %s in %d files (cap %s)\n- Removed %d files, freed %s\n", st.Root, humanBytes(st.Bytes), st.Files, limit, st.Removed, humanBytes(st.Freed))
		return nil
	}
	fmt.Printf("%s: %s in %d files (cap %s); removed %d files, freed %s\n", st.Root, humanBytes(st.Bytes), st.Files, limit, st.Removed, humanBytes(st.Freed))
	return nil
}
