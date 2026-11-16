package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baz-scm/gion/internal/cache"
)

// The registry has one file per workspace in cache.WorkspacesDir, holding
// the workspace's path. The file's mtime is when gion last ran there, so
// concurrent commands never race on a shared file.

func entryName(dir string) string {
	h := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(h[:8])
}

// rootOf returns the workspace directory containing dir, by stat alone
// (Find reads state.json, which can be large).
func rootOf(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, stateDir, "state.json")); err == nil {
			return d, true
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

// Touch records that gion ran in the workspace containing dir, if any, so
// GC knows it's in use. Best effort: errors are ignored.
func Touch(dir string) {
	d, ok := rootOf(dir)
	if !ok {
		return
	}
	reg, err := cache.WorkspacesDir()
	if err != nil {
		return
	}
	p := filepath.Join(reg, entryName(d))
	now := time.Now()
	if os.Chtimes(p, now, now) == nil {
		return
	}
	if os.MkdirAll(reg, 0o755) == nil {
		_ = os.WriteFile(p, []byte(d+"\n"), 0o644)
	}
}

// Kept is an idle workspace GC left in place, and why.
type Kept struct {
	Dir    string
	Reason string
}

// GCResult reports a workspace cleanup.
type GCResult struct {
	Removed []string // idle, clean workspaces deleted (or, dry run, to delete)
	Freed   int64
	Kept    []Kept // idle workspaces with work in them
	Active  int    // registered workspaces used within the idle window
}

// GC deletes registered workspaces unused for longer than idle that hold
// no work: no edits or new files anywhere in them (files .gitignore matches
// don't count), and no stopped rebase. Everything in such a workspace can
// be cloned again. Workspaces that are gone are dropped from the registry.
// With dryRun, nothing is deleted.
func GC(idle time.Duration, now time.Time, dryRun bool) (GCResult, error) {
	var res GCResult
	reg, err := cache.WorkspacesDir()
	if err != nil {
		return res, err
	}
	entries, err := os.ReadDir(reg)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	cwd, _ := os.Getwd()
	for _, e := range entries {
		p := filepath.Join(reg, e.Name())
		fi, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		dir := strings.TrimSpace(string(b))
		if d, ok := rootOf(dir); !ok || d != dir {
			if !dryRun {
				_ = os.Remove(p) // deleted or no longer a workspace
			}
			continue
		}
		if now.Sub(fi.ModTime()) < idle {
			res.Active++
			continue
		}
		if reason := busy(dir, cwd); reason != "" {
			res.Kept = append(res.Kept, Kept{Dir: dir, Reason: reason})
			continue
		}
		size := dirSize(dir)
		if !dryRun {
			if err := os.RemoveAll(dir); err != nil {
				res.Kept = append(res.Kept, Kept{Dir: dir, Reason: err.Error()})
				continue
			}
			_ = os.Remove(p)
		}
		res.Removed = append(res.Removed, dir)
		res.Freed += size
	}
	sort.Strings(res.Removed)
	sort.Slice(res.Kept, func(i, j int) bool { return res.Kept[i].Dir < res.Kept[j].Dir })
	return res, nil
}

// busy says why the workspace at dir must stay, or "" if it can go.
func busy(dir, cwd string) string {
	if cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator)) {
		return "current directory"
	}
	w, err := Find(dir)
	if err != nil {
		return err.Error()
	}
	if w.Rebasing() {
		return "rebase in progress"
	}
	changes, err := w.status(true)
	if err != nil {
		return err.Error()
	}
	if n := len(changes); n > 0 {
		s := "s"
		if n == 1 {
			s = ""
		}
		return fmt.Sprintf("%d local change%s", n, s)
	}
	return ""
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
