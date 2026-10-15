//go:build !unix

package cache

import "io/fs"

func diskSize(fi fs.FileInfo) int64 { return fi.Size() }
