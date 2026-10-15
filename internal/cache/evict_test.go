package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"0": 0, "123": 123, "64K": 64 << 10, "500MB": 500 << 20, "1G": 1 << 30,
		"1GB": 1 << 30, "1.5GiB": 3 << 29, "2t": 2 << 40, " 10m ": 10 << 20,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1G", "1X"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func writeAged(t *testing.T, p string, size int, age time.Duration, now time.Time) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	when := now.Add(-age)
	os.Chtimes(p, when, when)
}

func TestEvictOldestFirstAndKeepsRecent(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	const kb = 64 << 10 // big enough that block rounding doesn't matter
	writeAged(t, filepath.Join(root, "objects", "aa", "oldest"), kb, 72*time.Hour, now)
	writeAged(t, filepath.Join(root, "objects", "bb", "old"), kb, 48*time.Hour, now)
	writeAged(t, filepath.Join(root, "index", "x.idx.gz"), kb, 24*time.Hour, now)
	writeAged(t, filepath.Join(root, "objects", "cc", "recent"), kb, time.Minute, now)
	writeAged(t, filepath.Join(root, "objects", "tmp", "stale"), 1, 2*time.Hour, now)

	before, err := Evict(root, 0, now) // max 0: report only
	if err != nil || before.Files != 4 {
		t.Fatalf("report: %+v %v", before, err)
	}

	// Cap at 2.5 files: target is 80% = 2 files, so the two oldest go.
	st, err := Evict(root, kb*5/2, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"objects/aa/oldest", "objects/bb/old"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be evicted", gone)
		}
	}
	for _, kept := range []string{"index/x.idx.gz", "objects/cc/recent"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s should be kept", kept)
		}
	}
	if st.Files != 2 {
		t.Errorf("files left = %d, want 2", st.Files)
	}

	// Even a tiny cap never evicts files used in the last 10 minutes.
	st, _ = Evict(root, 1, now)
	if _, err := os.Stat(filepath.Join(root, "objects", "cc", "recent")); err != nil {
		t.Error("recent file evicted")
	}
	if st.Files != 1 {
		t.Errorf("files left = %d, want 1 (the recent one)", st.Files)
	}
}

func TestMaybeEvictIsThrottled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GION_CACHE", filepath.Join(root, "objects"))
	t.Setenv("GION_CACHE_MAX", "1")
	now := time.Now()
	old := filepath.Join(root, "objects", "aa", "old")

	writeAged(t, old, 4096, 48*time.Hour, now)
	MaybeEvict()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("first MaybeEvict should evict")
	}
	writeAged(t, old, 4096, 48*time.Hour, now)
	MaybeEvict() // within the hour: skipped
	if _, err := os.Stat(old); err != nil {
		t.Error("second MaybeEvict within an hour should not run")
	}
}

func TestGetRefreshesStaleMtime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "objects")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	o := s.New()
	o.SetType(3) // blob
	w, _ := o.Writer()
	w.Write([]byte("x"))
	w.Close()
	if err := s.Put(o); err != nil {
		t.Fatal(err)
	}
	p := s.path(o.Hash())
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(p, old, old)
	if _, err := s.Get(o.Hash()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if time.Since(fi.ModTime()) > time.Minute {
		t.Error("Get should refresh a stale mtime for LRU")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "CACHEDIR.TAG")); err != nil {
		t.Error("CACHEDIR.TAG not written")
	}
}
