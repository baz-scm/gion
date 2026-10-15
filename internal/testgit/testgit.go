// Package testgit serves a seeded bare repo over git:// for integration
// tests. The git CLI is a test fixture only; `gion` never shells out to it.
package testgit

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run runs git in dir and returns trimmed output.
func Run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Serve seeds a bare repo and serves it with git daemon. It returns the
// remote URL and a working clone for simulating other writers.
func Serve(t *testing.T) (remote, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	Run(t, dir, "init", "-q", "-b", "main", seed)
	for p, c := range map[string]string{"a.txt": "a\n", "b.txt": "b\n", "d/e.txt": "e\n", "d/f/g.txt": "g\n", "run.sh": "#!/bin/sh\n"} {
		os.MkdirAll(filepath.Join(seed, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(seed, p), []byte(c), 0o644)
	}
	os.Chmod(filepath.Join(seed, "run.sh"), 0o755)
	Run(t, seed, "add", "-A")
	Run(t, seed, "commit", "-qm", "seed")
	srv := filepath.Join(dir, "srv")
	os.MkdirAll(srv, 0o755)
	bare := filepath.Join(srv, "r.git")
	Run(t, dir, "clone", "-q", "--bare", seed, bare)
	Run(t, bare, "config", "uploadpack.allowFilter", "true")
	Run(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	d := exec.Command("git", "daemon", "--reuseaddr", "--export-all", "--enable=receive-pack",
		"--base-path="+srv, "--listen=127.0.0.1", fmt.Sprintf("--port=%d", port), srv)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Process.Kill(); d.Wait() })
	remote = fmt.Sprintf("git://127.0.0.1:%d/r.git", port)
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	clone = filepath.Join(dir, "clone")
	Run(t, dir, "clone", "-q", remote, clone)
	return remote, clone
}
