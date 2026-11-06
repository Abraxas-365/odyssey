package srcarchive

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strconv"
)

// ContentVersion prefixes every content digest: a change to what the digest
// covers bumps it, so no old digest ever equals a new one by accident.
const ContentVersion = "c1"

// contents collects what a folder's content digest covers, entry by entry:
// each regular file's path, whether it is executable and its bytes' SHA-256,
// and each link's path and target. Directories, times, owners, permission
// bits other than the executable one and the archive's order and
// compression do not count: two archives of the same files have the same
// digest whatever tool made them.
type contents struct {
	entries []string
}

// file records a regular file whose bytes hash to sum.
func (c *contents) file(hdr *tar.Header, name string, sum []byte) {
	mode := "f"
	if hdr.Mode&0o111 != 0 {
		mode = "x"
	}
	c.entries = append(c.entries, mode+" "+strconv.Quote(name)+" "+hex.EncodeToString(sum))
}

// link records a symlink.
func (c *contents) link(name, target string) {
	c.entries = append(c.entries, "l "+strconv.Quote(name)+" "+strconv.Quote(target))
}

// add records one entry of walk, reading a regular file's body. A body that
// cannot be read is left to walk, which reports it as it does any other.
func (c *contents) add(hdr *tar.Header, name string, body io.Reader) {
	switch hdr.Typeflag {
	case tar.TypeReg:
		h := sha256.New()
		if _, err := io.Copy(h, body); err == nil {
			c.file(hdr, name, h.Sum(nil))
		}
	case tar.TypeSymlink:
		c.link(name, hdr.Linkname)
	}
}

// digest is "c1:<sha256 hex>" of the sorted entries.
func (c *contents) digest() string {
	sort.Strings(c.entries)
	h := sha256.New()
	for _, e := range c.entries {
		_, _ = io.WriteString(h, e)
		_, _ = h.Write([]byte{'\n'})
	}
	return ContentVersion + ":" + hex.EncodeToString(h.Sum(nil))
}
