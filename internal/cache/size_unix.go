//go:build unix

package cache

import (
	"io/fs"
	"syscall"
)

// diskSize is the space a file really takes: loose objects are tiny, so
// whole blocks matter far more than byte counts.
func diskSize(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}
