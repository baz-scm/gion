package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	SetGuide("---\nname: gion\n---\n\n# guide v1\n")
	t.Cleanup(func() { SetGuide("") })
	ctx := context.Background()

	if err := skill(ctx, []string{"install"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range skillDirs {
		b, err := os.ReadFile(filepath.Join(home, d, "gion", "SKILL.md"))
		if err != nil || !strings.Contains(string(b), "name: gion") {
			t.Fatalf("%s: %q, %v", d, b, err)
		}
	}

	// A new binary's guide replaces the old one.
	SetGuide("---\nname: gion\n---\n\n# guide v2\n")
	if err := skill(ctx, []string{"install"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude/skills/gion/SKILL.md"))
	if !strings.Contains(string(b), "v2") {
		t.Fatalf("not updated: %q", b)
	}

	// --project writes under the current directory, not home.
	proj := t.TempDir()
	t.Chdir(proj)
	if err := skill(ctx, []string{"install", "--project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".agents/skills/gion/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if agentGuide() != "# guide v2\n" {
		t.Fatalf("agentGuide = %q", agentGuide())
	}
}

func TestCommonDir(t *testing.T) {
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{[]string{"a/b/c", "a/b/d"}, "a/b"},
		{[]string{"a/b/c", "a/x"}, "a"},
		{[]string{"a/b", "c"}, ""},
		{[]string{"a/b/c"}, "a/b"},
		{[]string{"a/b", "a/b/c"}, "a/b"},
	} {
		if got := commonDir(c.paths); got != c.want {
			t.Errorf("commonDir(%v) = %q, want %q", c.paths, got, c.want)
		}
	}
}
