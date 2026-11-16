package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/workspace"
)

func gc(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	maxFlag := fs.String("max", "", "size cap for this run (default $GION_CACHE_MAX or 1GB)")
	idleFlag := fs.String("idle", "7d", "delete clean workspaces unused for this long (e.g. 3d, 12h; 0 keeps all)")
	dryRun := fs.Bool("dry-run", false, "list the workspaces gc would delete, delete nothing (the cache is still trimmed)")
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
	idle, err := parseIdle(*idleFlag)
	if err != nil {
		return err
	}
	root, err := cache.Root()
	if err != nil {
		return err
	}
	st, err := cache.Evict(root, max, time.Now())
	if err != nil {
		return err
	}
	var ws workspace.GCResult
	if idle > 0 {
		if ws, err = workspace.GC(idle, time.Now(), *dryRun); err != nil {
			return err
		}
	}
	limit := "no limit"
	if max > 0 {
		limit = humanBytes(max)
	}
	verb := "removed"
	if *dryRun {
		verb = "would remove"
	}
	if *md {
		fmt.Printf("## Cache `%s`\n\n- Size: %s in %d files (cap %s)\n- Removed %d files, freed %s\n", st.Root, humanBytes(st.Bytes), st.Files, limit, st.Removed, humanBytes(st.Freed))
		if idle > 0 {
			fmt.Printf("\n## Workspaces idle over %s\n\n- %s %d, freeing %s; %d in use\n", *idleFlag, capitalize(verb), len(ws.Removed), humanBytes(ws.Freed), ws.Active)
			for _, d := range ws.Removed {
				fmt.Printf("- %s `%s`\n", verb, d)
			}
			for _, k := range ws.Kept {
				fmt.Printf("- kept `%s`: %s\n", k.Dir, k.Reason)
			}
		}
		return nil
	}
	fmt.Printf("%s: %s in %d files (cap %s); removed %d files, freed %s\n", st.Root, humanBytes(st.Bytes), st.Files, limit, st.Removed, humanBytes(st.Freed))
	if idle > 0 {
		fmt.Printf("workspaces idle over %s: %s %d, freeing %s; %d in use\n", *idleFlag, verb, len(ws.Removed), humanBytes(ws.Freed), ws.Active)
		for _, d := range ws.Removed {
			fmt.Printf("  %s %s\n", verb, d)
		}
		for _, k := range ws.Kept {
			fmt.Printf("  kept %s: %s\n", k.Dir, k.Reason)
		}
	}
	return nil
}

// parseIdle reads a duration, also accepting days ("7d").
func parseIdle(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(d, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("--idle %q: want a duration like 7d or 12h", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("--idle %q: want a duration like 7d or 12h", s)
	}
	return d, nil
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
