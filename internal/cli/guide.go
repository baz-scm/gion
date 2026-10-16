package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var guide string

// SetGuide sets the agent guide (the skill file) printed by `help --md`.
func SetGuide(g string) { guide = g }

// agentGuide returns the guide without its YAML front matter.
func agentGuide() string {
	if rest, ok := strings.CutPrefix(guide, "---\n"); ok {
		if _, body, ok := strings.Cut(rest, "\n---\n"); ok {
			return strings.TrimLeft(body, "\n")
		}
	}
	return guide
}

// skillDirs are where agents look for skills, relative to the home or project
// directory: Claude Code's own, and the shared .agents/skills that Codex,
// Cursor, Amp, Cline and others read.
var skillDirs = []string{".claude/skills", ".agents/skills"}

// skill installs the embedded skill file for agents.
func skill(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("skill", flag.ContinueOnError)
	project := fs.Bool("project", false, "install into the current directory instead of your home")
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "install" {
		return fmt.Errorf("usage: %s skill install [--project]", prog)
	}
	if guide == "" {
		return errors.New("this build has no embedded skill")
	}
	root := "."
	if !*project {
		if root, err = os.UserHomeDir(); err != nil {
			return err
		}
	}
	type result struct{ path, what string }
	var done []result
	for _, d := range skillDirs {
		p := filepath.Join(root, filepath.FromSlash(d), "gion", "SKILL.md")
		what := "installed"
		if old, err := os.ReadFile(p); err == nil {
			what = "updated"
			if bytes.Equal(old, []byte(guide)) {
				what = "up to date"
			}
		}
		if what != "up to date" {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(guide), 0o644); err != nil {
				return err
			}
		}
		done = append(done, result{p, what})
	}
	if *md {
		fmt.Printf("## gion skill (%s)\n\n", Version())
		for _, r := range done {
			fmt.Printf("- %s: `%s`\n", r.what, r.path)
		}
		fmt.Println("\nAgents pick it up in their next session.")
		return nil
	}
	for _, r := range done {
		fmt.Printf("%s\t%s\n", r.what, r.path)
	}
	return nil
}
