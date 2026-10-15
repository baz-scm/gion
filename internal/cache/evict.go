package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultMax is the cache size cap unless GION_CACHE_MAX says otherwise.
const DefaultMax = 1 << 30

const (
	// protect keeps recently used files: another gion process may be
	// between fetching an object and reading it.
	protect = 10 * time.Minute
	// every limits automatic eviction to one size check per interval, since
	// measuring means walking every file.
	every     = time.Hour
	stampFile = ".last-gc"
)

// MaxSize reads GION_CACHE_MAX ("1GB", "500M", "0" for no limit).
func MaxSize() (int64, error) {
	v := os.Getenv("GION_CACHE_MAX")
	if v == "" {
		return DefaultMax, nil
	}
	n, err := ParseSize(v)
	if err != nil {
		return 0, fmt.Errorf("GION_CACHE_MAX: %w", err)
	}
	return n, nil
}

// ParseSize parses "123", "64K", "500MB", "1G", "1.5GiB" (powers of 1024).
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "IB"), "B")
	mult := float64(1)
	if t != "" {
		switch t[len(t)-1] {
		case 'K':
			mult, t = 1<<10, t[:len(t)-1]
		case 'M':
			mult, t = 1<<20, t[:len(t)-1]
		case 'G':
			mult, t = 1<<30, t[:len(t)-1]
		case 'T':
			mult, t = 1<<40, t[:len(t)-1]
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(f * mult), nil
}

// Stats describes the cache before and after an eviction.
type Stats struct {
	Root    string
	Max     int64
	Files   int   // files left
	Bytes   int64 // disk usage left
	Removed int
	Freed   int64
}

type file struct {
	path  string
	size  int64
	mtime time.Time
}

// Evict trims root to 80% of max, least recently used first, never touching
// files used within the last 10 minutes. Abandoned temp files (an hour old)
// always go. max <= 0 only reports usage.
func Evict(root string, max int64, now time.Time) (Stats, error) {
	st := Stats{Root: root, Max: max}
	var files []file
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if name := d.Name(); name == "CACHEDIR.TAG" || name == stampFile {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil // removed concurrently
		}
		f := file{path: p, size: diskSize(fi), mtime: fi.ModTime()}
		if filepath.Base(filepath.Dir(p)) == "tmp" && now.Sub(f.mtime) > time.Hour {
			if os.Remove(p) == nil {
				st.Removed++
				st.Freed += f.size
			}
			return nil
		}
		files = append(files, f)
		st.Bytes += f.size
		return nil
	})
	if err != nil {
		return st, err
	}
	st.Files = len(files)

	if max > 0 && st.Bytes > max {
		sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
		target := max / 10 * 8
		for _, f := range files {
			if st.Bytes <= target || now.Sub(f.mtime) < protect {
				break // sorted oldest first: the rest are newer still
			}
			if os.Remove(f.path) != nil {
				continue
			}
			st.Bytes -= f.size
			st.Freed += f.size
			st.Files--
			st.Removed++
		}
	}
	if err := os.MkdirAll(root, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(root, stampFile), nil, 0o644)
	}
	return st, nil
}

// MaybeEvict runs Evict on the configured cache if the last check is more
// than an hour old. Errors are ignored: the cache is best-effort.
func MaybeEvict() {
	root, err := Root()
	if err != nil {
		return
	}
	max, err := MaxSize()
	if err != nil || max <= 0 {
		return
	}
	if fi, err := os.Stat(filepath.Join(root, stampFile)); err == nil && time.Since(fi.ModTime()) < every {
		return
	}
	if _, err := os.Stat(root); err != nil {
		return // nothing cached yet
	}
	_, _ = Evict(root, max, time.Now())
}
