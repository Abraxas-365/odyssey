package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sort"
	"sync"
	"time"
)

// Bundle is the skill packed for download.
type Bundle struct {
	// Archive is the tar.gz; its single top directory is odicea/.
	Archive []byte
	// Version is the first 12 hex digits of the archive's sha256: it changes
	// exactly when a file does.
	Version string
}

// bundleTime is every entry's mtime: fixed, so the same files always give
// the same bytes (and the same ETag on every replica and restart).
var bundleTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Build packs FS deterministically: sorted entries, fixed mtime, no owner,
// modes 0644 (files) and 0755 (directories), gzip without a header time.
func Build() (Bundle, error) { return pack(FS, Name) }

func pack(fsys fs.FS, root string) (Bundle, error) {
	var paths []string
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		return Bundle{}, err
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return Bundle{}, err
	}
	tw := tar.NewWriter(zw)
	for _, p := range paths {
		info, err := fs.Stat(fsys, p)
		if err != nil {
			return Bundle{}, err
		}
		h := &tar.Header{Name: p, ModTime: bundleTime, Format: tar.FormatPAX}
		if info.IsDir() {
			h.Name += "/"
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
			err = tw.WriteHeader(h)
		} else {
			var data []byte
			if data, err = fs.ReadFile(fsys, p); err != nil {
				return Bundle{}, err
			}
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, int64(len(data))
			if err = tw.WriteHeader(h); err == nil {
				_, err = tw.Write(data)
			}
		}
		if err != nil {
			return Bundle{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Bundle{}, err
	}
	if err := zw.Close(); err != nil {
		return Bundle{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return Bundle{Archive: buf.Bytes(), Version: hex.EncodeToString(sum[:])[:12]}, nil
}

var built = sync.OnceValues(Build)

// Default is the bundle of the embedded skill, packed once per process. The
// files are compiled in, so a failure is a programming error.
func Default() Bundle {
	b, err := built()
	if err != nil {
		panic("skills: pack the embedded skill: " + err.Error())
	}
	return b
}
