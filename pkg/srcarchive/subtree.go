package srcarchive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"path"
	"strings"
)

// ErrRootMissing is Subtree's answer when nothing lives under the folder.
var ErrRootMissing = errors.New("srcarchive: the folder does not exist in the source")

// ValidRoot reports whether root names a folder inside a source: a clean,
// relative path without "." or ".." parts ("" is not a folder).
func ValidRoot(root string) bool {
	return root != "" && len(root) <= maxPath && root == path.Clean(root) && root != "." && root != ".." &&
		!strings.HasPrefix(root, "/") && !strings.HasPrefix(root, "../") && !strings.ContainsAny(root, "\\\x00")
}

// Subtree reads a source archive from r, checking it with the same rules as
// the Verifier, and writes to w a source archive of the folder root only,
// re-rooted so its contents sit at the archive's root (a monorepo's
// "apps/web" becomes a source of its own). A link under root that points
// outside it is refused: the folder is all the build will have.
//
// The summary is the written archive's (its compressed size and SHA-256
// included). Errors: *Invalid for an archive or a folder that breaks a rule,
// *TooLarge when the written archive exceeds limits.Compressed,
// ErrRootMissing when nothing lives under root; errors writing to w are
// returned as they are. After an error, w's contents are not meaningful.
func Subtree(r io.Reader, w io.Writer, root string, limits Limits) (Summary, error) {
	if !ValidRoot(root) {
		return Summary{}, invalid("the folder is not a valid relative path: " + safe(root))
	}
	out := &countingWriter{w: w, hash: sha256.New(), left: limits.Compressed, limit: limits.Compressed}
	zw := gzip.NewWriter(out)
	tw := tar.NewWriter(zw)
	prefix := root + "/"
	var sum Summary
	entries := 0
	err := walk(r, limits, func(hdr *tar.Header, name string, body io.Reader) error {
		if !strings.HasPrefix(name, prefix) {
			return nil // outside the folder (or the folder itself)
		}
		rel := name[len(prefix):]
		oh := &tar.Header{Typeflag: hdr.Typeflag, Name: rel, Mode: hdr.Mode & 0o777, ModTime: hdr.ModTime, Format: tar.FormatPAX}
		switch hdr.Typeflag {
		case tar.TypeDir:
			oh.Name += "/"
		case tar.TypeSymlink:
			if err := checkLink(rel, hdr.Linkname); err != nil {
				return invalid("a link in the folder " + safe(root) + " points outside it: " + safe(rel))
			}
			oh.Linkname = hdr.Linkname
		case tar.TypeReg:
			oh.Size = hdr.Size
		}
		if err := tw.WriteHeader(oh); err != nil {
			return out.failure(err)
		}
		entries++
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, body); err != nil {
				return out.failure(err)
			}
			sum.Files++
			sum.UnpackedSize += hdr.Size
		}
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	if entries == 0 {
		return Summary{}, ErrRootMissing
	}
	if err := tw.Close(); err != nil {
		return Summary{}, out.failure(err)
	}
	if err := zw.Close(); err != nil {
		return Summary{}, out.failure(err)
	}
	sum.Size, sum.SHA256 = out.size, hex.EncodeToString(out.hash.Sum(nil))
	return sum, nil
}

// countingWriter hashes and counts what reaches w, refusing more than limit.
type countingWriter struct {
	w           io.Writer
	hash        hash.Hash
	size        int64
	left, limit int64
	over        bool
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.over = true
		return 0, errTooLarge
	}
	n, err := c.w.Write(p)
	c.hash.Write(p[:n])
	c.size += int64(n)
	c.left -= int64(n)
	return n, err
}

var errTooLarge = errors.New("srcarchive: the folder's archive is too large")

// failure is the error to report for err from the writing side.
func (c *countingWriter) failure(err error) error {
	if c.over {
		return &TooLarge{Limit: c.limit}
	}
	return err
}
