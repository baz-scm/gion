package workspace

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/go-git/go-git/v6/plumbing"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	"github.com/go-git/go-git/v6/plumbing/format/objfile"
)

// packAbove is how many new objects make writeGit pack them into one file
// instead of writing loose objects: a file per object costs a syscall or
// more each, which adds up to minutes for a big workspace. Tests lower it.
var packAbove = 64

// gitObjects collects the objects .git lacks, then writes them all at once.
type gitObjects struct {
	w      *Workspace
	packed map[plumbing.Hash]bool // in .git/objects/pack
	queued map[plumbing.Hash]bool
	queue  []pendingObject
}

// pendingObject has its content in data, or a path to read it from: a loose
// object in the gion cache, or a workspace file.
type pendingObject struct {
	hash  plumbing.Hash
	typ   plumbing.ObjectType
	data  []byte
	loose string // cache file to read, or link to when writing loose
	file  string // workspace file holding a blob's content
}

func (w *Workspace) newGitObjects() (*gitObjects, error) {
	o := &gitObjects{w: w, packed: map[plumbing.Hash]bool{}, queued: map[plumbing.Hash]bool{}}
	idxs, err := filepath.Glob(w.gitPath("objects", "pack", "*.idx"))
	if err != nil {
		return nil, err
	}
	for _, p := range idxs {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		idx := idxfile.NewMemoryIndex(sha1.Size)
		err = idxfile.NewDecoder(f, sha1.New()).Decode(idx)
		f.Close()
		if err != nil {
			return nil, err
		}
		it, err := idx.Entries()
		if err != nil {
			return nil, err
		}
		for {
			e, err := it.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			o.packed[e.Hash] = true
		}
	}
	return o, nil
}

func (o *gitObjects) has(h plumbing.Hash) bool {
	if o.queued[h] || o.packed[h] {
		return true
	}
	_, err := os.Stat(o.w.gitObjectPath(h))
	return err == nil
}

func (o *gitObjects) add(p pendingObject) {
	if o.has(p.hash) {
		return
	}
	o.queued[p.hash] = true
	o.queue = append(o.queue, p)
}

// load returns p's content, or nil if it can't be had (or isn't what it
// should be).
func (p pendingObject) load() ([]byte, error) {
	if p.data != nil {
		return p.data, nil
	}
	var data []byte
	if p.loose != "" {
		f, err := os.Open(p.loose)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r, err := objfile.NewReader(f, format.SHA1)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		if _, _, err := r.Header(); err != nil {
			return nil, err
		}
		if data, err = io.ReadAll(r); err != nil {
			return nil, err
		}
	} else {
		fi, err := os.Lstat(p.file)
		if err != nil {
			return nil, err
		}
		if data, _, err = readDisk(p.file, fi); err != nil {
			return nil, err
		}
	}
	if blobHash(data) != p.hash {
		return nil, nil
	}
	return data, nil
}

// flush writes the queued objects: one pack when there are many, else loose
// (blobs hard-linked from the cache).
func (o *gitObjects) flush() error {
	defer func() { o.queue = nil }()
	if len(o.queue) > packAbove {
		return o.writePack()
	}
	for _, p := range o.queue {
		if p.loose != "" {
			dst := o.w.gitObjectPath(p.hash)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Link(p.loose, dst); err == nil || errors.Is(err, fs.ErrExist) {
				continue
			}
		}
		data, err := p.load()
		if err != nil || data == nil {
			continue // unavailable: only `git diff` of that file suffers
		}
		if err := o.w.gitPutRaw(p.hash, p.typ, data); err != nil {
			return err
		}
	}
	return nil
}

// writePack writes the queue as .git/objects/pack/pack-<sum>.{pack,idx}.
func (o *gitObjects) writePack() error {
	dir := o.w.gitPath("objects", "pack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "tmp_pack_")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	defer tmp.Close()

	bw := bufio.NewWriterSize(tmp, 1<<20)
	// The object count goes in once known, then the trailer is a checksum of
	// the whole file.
	hdr := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	if _, err := bw.Write(hdr); err != nil {
		return err
	}
	// Entries may come in any order, so workers load and compress them in
	// parallel and this goroutine appends each as it's ready.
	type packed struct {
		hash  plumbing.Hash
		entry []byte
	}
	jobs, done := make(chan pendingObject), make(chan packed)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			zw, _ := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
			for p := range jobs {
				data, err := p.load()
				if err != nil || data == nil {
					continue // unavailable: only `git diff` of that file suffers
				}
				buf.Reset()
				writePackHeader(&buf, p.typ, len(data))
				zw.Reset(&buf)
				zw.Write(data) // writes to a bytes.Buffer don't fail
				zw.Close()
				done <- packed{p.hash, bytes.Clone(buf.Bytes())}
			}
		}()
	}
	go func() {
		for _, p := range o.queue {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(done)
	}()
	iw := new(idxfile.Writer)
	off, n := uint64(len(hdr)), uint32(0)
	var werr error
	for e := range done {
		if werr != nil {
			continue // drain so the workers finish
		}
		iw.Add(e.hash, off, crc32.ChecksumIEEE(e.entry))
		_, werr = bw.Write(e.entry)
		off += uint64(len(e.entry))
		n++
	}
	if werr != nil {
		return werr
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	binary.BigEndian.PutUint32(hdr[8:], n)
	if _, err := tmp.WriteAt(hdr, 0); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha1.New()
	if _, err := io.Copy(h, tmp); err != nil {
		return err
	}
	sum := h.Sum(nil)
	if _, err := tmp.Write(sum); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	checksum, _ := plumbing.FromBytes(sum)
	if err := iw.OnFooter(checksum); err != nil {
		return err
	}
	idx, err := iw.Index()
	if err != nil {
		return err
	}

	// The pack goes in place first: git only looks at packs with an index.
	base := filepath.Join(dir, "pack-"+checksum.String())
	if err := os.Rename(tmp.Name(), base+".pack"); err != nil {
		return err
	}
	var ib bytes.Buffer
	if err := idxfile.Encode(&ib, sha1.New(), idx); err != nil {
		return err
	}
	return writeAtomic(base+".idx", ib.Bytes())
}

// writePackHeader writes a pack entry's type and size varint.
func writePackHeader(w *bytes.Buffer, t plumbing.ObjectType, size int) {
	b := byte(t)<<4 | byte(size&0x0f)
	size >>= 4
	for size > 0 {
		w.WriteByte(b | 0x80)
		b = byte(size & 0x7f)
		size >>= 7
	}
	w.WriteByte(b)
}

// queueBlob queues p's base blob: from the object cache when it's there,
// else from disk if the file is still as gion wrote it. An edited file
// whose blob is gone is left out; only `git diff` on it suffers.
func (w *Workspace) queueBlob(o *gitObjects, p, cacheDir string) {
	f := w.State.Files[p]
	oid, _ := plumbing.FromHex(f.OID)
	if o.has(oid) {
		return
	}
	if cacheDir != "" {
		src := filepath.Join(cacheDir, f.OID[:2], f.OID[2:])
		if _, err := os.Stat(src); err == nil {
			o.add(pendingObject{hash: oid, typ: plumbing.BlobObject, loose: src})
			return
		}
	}
	fi, err := os.Lstat(w.abs(p))
	if err != nil || fi.Size() != f.Size || fi.ModTime().UnixNano() != f.MTime {
		return
	}
	o.add(pendingObject{hash: oid, typ: plumbing.BlobObject, file: w.abs(p)})
}
