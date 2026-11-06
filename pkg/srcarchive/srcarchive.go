// Package srcarchive checks a source (a folder as .tar.gz) while it streams,
// and extracts one safely. Both planes use it, so it imports nothing of the
// repository: the API verifies on upload (internal/build/archive wraps it)
// and the cluster's builder verifies again before building
// (`odyssey-agent builder prepare`), with the same rules:
//
//   - gzip-compressed tar, within Limits (compressed size, expanded size,
//     entries);
//   - only regular files, directories and symlinks that stay inside the
//     folder; no devices, FIFOs, hard links or sparse files;
//   - relative, clean paths: no absolute paths, "..", backslashes or NUL;
//   - nothing after the end of the gzip stream.
package srcarchive

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Limits bound an archive.
type Limits struct {
	Compressed int64
	Unpacked   int64
	Files      int
}

// Platform are Odyssey's limits of a source. The upload enforces them
// (internal/build's MaxSource* constants equal them, a test checks) and the
// builder enforces them again.
func Platform() Limits {
	return Limits{Compressed: 100 << 20, Unpacked: 500 << 20, Files: 20000}
}

// maxPath bounds an entry's path.
const maxPath = 4096

// Summary is what a verified archive holds.
type Summary struct {
	Size         int64  // compressed bytes
	SHA256       string // of the compressed bytes, hex
	Files        int    // regular files
	UnpackedSize int64  // bytes of the regular files
	// ContentDigest identifies the files themselves ("c1:<sha256 hex>"; see
	// contents): the same folder packed again, by any tool, at any time, has
	// the same one, which SHA256 does not.
	ContentDigest string
}

// Invalid is the refusal of an archive that breaks a rule. Message is for
// people (in English) and names the offending path, made printable.
type Invalid struct{ Message string }

func (e *Invalid) Error() string { return e.Message }

// TooLarge is the refusal of an archive over the compressed limit.
type TooLarge struct{ Limit int64 }

func (e *TooLarge) Error() string { return "the source archive is larger than " + MiB(e.Limit) }

func invalid(msg string) error { return &Invalid{Message: msg} }

// MiB words a byte count in whole MiB.
func MiB(n int64) string { return mib(n) }

func mib(n int64) string {
	return itoa(n>>20) + " MiB"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Verifier passes an archive through while checking it: reading it returns
// the same bytes, and the read that would end the stream returns the
// verdict instead of io.EOF when the archive is not acceptable. A consumer
// that stores what it reads (blob.Store.Put) therefore stores only valid
// archives. Close it when done (it stops the checker if reading stopped early).
type Verifier struct {
	src    io.Reader
	limits Limits
	hash   hash.Hash
	size   int64
	pw     *io.PipeWriter
	done   chan struct{}
	result Summary
	err    error // the checker's verdict, set before done closes
	ended  bool
	final  error // what every Read returns once ended
}

// NewVerifier checks r against limits as it is read.
func NewVerifier(r io.Reader, limits Limits) *Verifier {
	pr, pw := io.Pipe()
	v := &Verifier{src: r, limits: limits, hash: sha256.New(), pw: pw, done: make(chan struct{})}
	go func() {
		defer close(v.done)
		v.result, v.err = check(pr, limits)
		// Unblock the writer whatever happened: with the verdict, or by
		// draining what is left (trailing bytes after the gzip stream).
		if v.err != nil {
			pr.CloseWithError(v.err)
			return
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	return v
}

func (v *Verifier) Read(p []byte) (int, error) {
	if v.ended {
		return 0, v.final
	}
	n, err := v.src.Read(p)
	if n > 0 {
		v.size += int64(n)
		if v.size > v.limits.Compressed {
			return 0, v.abort(&TooLarge{Limit: v.limits.Compressed}, true)
		}
		v.hash.Write(p[:n])
		if _, werr := v.pw.Write(p[:n]); werr != nil {
			// The checker stopped early: its verdict, or the reason it was stopped.
			return 0, v.abort(werr, false)
		}
	}
	switch {
	case errors.Is(err, io.EOF):
		v.pw.Close()
		<-v.done
		v.ended, v.final = true, io.EOF
		if v.err != nil {
			v.final = v.err
		}
		return n, v.final
	case err != nil:
		// The upload itself broke: report that, not the checker's view of
		// the truncated stream.
		return n, v.abort(err, true)
	}
	return n, nil
}

// abort stops the checker and ends the stream with err, or with the
// checker's verdict when it has one (the archive is bad) and !own.
func (v *Verifier) abort(err error, own bool) error {
	v.pw.CloseWithError(err)
	<-v.done
	v.ended, v.final = true, err
	if v.err != nil && !own && !errors.Is(v.err, err) {
		v.final = v.err
	}
	return v.final
}

// Summary is the verified archive's summary; valid once Read returned io.EOF.
func (v *Verifier) Summary() Summary {
	s := v.result
	s.Size = v.size
	s.SHA256 = hex.EncodeToString(v.hash.Sum(nil))
	return s
}

// Close stops the checker (a no-op after the end of the stream).
func (v *Verifier) Close() error {
	if !v.ended {
		v.abort(errors.New("archive: reading stopped"), true)
	}
	return nil
}

// check reads a whole .tar.gz from r, applying the rules.
func check(r io.Reader, limits Limits) (Summary, error) {
	var s Summary
	var c contents
	err := walk(r, limits, func(hdr *tar.Header, name string, body io.Reader) error {
		if hdr.Typeflag == tar.TypeReg {
			s.Files++
			s.UnpackedSize += hdr.Size
		}
		c.add(hdr, name, body)
		return nil
	})
	if err == nil {
		s.ContentDigest = c.digest()
	}
	return s, err
}

// walk reads every entry of the archive, checking each, and hands it (with
// its cleaned name) to fn. Regular files' bodies are consumed and counted
// even if fn does not read them.
func walk(r io.Reader, limits Limits, fn func(hdr *tar.Header, name string, body io.Reader) error) error {
	// Our own buffer, shared with gzip (a ByteReader is used as is), so what
	// follows the gzip stream can be read after it.
	br := bufio.NewReader(r)
	gz, err := gzip.NewReader(br)
	if err != nil {
		return invalid("the source must be a .tar.gz archive")
	}
	gz.Multistream(false)
	unpacked := &budget{r: gz, left: limits.Unpacked}
	tr := tar.NewReader(unpacked)
	entries := 0
	var declared int64           // sum of the entries' sizes
	links := map[string]string{} // symlink name → target
	seen := map[string]byte{}
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if unpacked.exceeded {
				return invalid("the source expands to more than " + mib(limits.Unpacked))
			}
			return invalid("the source archive is damaged: " + err.Error())
		}
		entries++
		if entries > limits.Files {
			return invalid("the source has more than " + itoa(int64(limits.Files)) + " files")
		}
		name, err := cleanName(hdr.Name)
		if err != nil {
			return err
		}
		// Sparse files expand holes the budget never reads (a few KB
		// claiming terabytes): refused, as is any size over what is left.
		if sparse(hdr) {
			return invalid("the source may not hold sparse files: " + safe(name))
		}
		if hdr.Size > limits.Unpacked-declared {
			return invalid("the source expands to more than " + mib(limits.Unpacked))
		}
		declared += hdr.Size
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink:
			if err := checkLink(name, hdr.Linkname); err != nil {
				return err
			}
		default:
			return invalid("the source may hold only files, folders and links inside it: " + safe(name))
		}
		if name == "" {
			continue // the root folder itself ("./")
		}
		if hdr.Typeflag == tar.TypeSymlink {
			links[name] = hdr.Linkname
		}
		if prev, dup := seen[name]; dup && (prev != tar.TypeDir || hdr.Typeflag != tar.TypeDir) {
			return invalid("the source has the same path twice: " + safe(name))
		}
		seen[name] = hdr.Typeflag
		names = append(names, name)
		if err := fn(hdr, name, tr); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			if unpacked.exceeded {
				return invalid("the source expands to more than " + mib(limits.Unpacked))
			}
			return invalid("the source archive is damaged: " + err.Error())
		}
	}
	// Read to the end of the gzip stream: it checks the CRC and the size.
	if _, err := io.Copy(io.Discard, unpacked); err != nil {
		if unpacked.exceeded {
			return invalid("the source expands to more than " + mib(limits.Unpacked))
		}
		return invalid("the source archive is damaged: " + err.Error())
	}
	// Nothing may follow it (another gzip member, junk): what is stored is
	// exactly what was checked.
	if _, err := br.ReadByte(); err == nil {
		return invalid("the source archive has data after its end")
	}
	if entries == 0 {
		return invalid("the source archive is empty")
	}
	return checkThroughLinks(names, links)
}

// checkThroughLinks refuses entries and link targets that pass through a
// link: only a path's last component may be one. Each link is checked
// lexically on its own (checkLink), so a chain of links ending in one stays
// inside the folder, while "l → sub, sub → ., a → l/.." (lexically inside,
// really outside) is refused.
func checkThroughLinks(names []string, links map[string]string) error {
	if len(links) == 0 {
		return nil
	}
	for _, name := range names {
		if through(path.Dir(name), links) {
			return invalid("a path in the source goes through a link: " + safe(name))
		}
	}
	for name, target := range links {
		// Walk the target's components from the link's folder, without
		// cleaning first: "l/.." must look into l.
		cur := path.Dir(name)
		parts := strings.Split(target, "/")
		for i, part := range parts {
			switch part {
			case "", ".":
				continue
			case "..":
				cur = path.Dir(cur)
				continue
			}
			cur = path.Join(cur, part)
			if _, isLink := links[cur]; isLink && i < len(parts)-1 {
				return invalid("a link in the source goes through another link: " + safe(name))
			}
		}
		if through(path.Dir(cur), links) {
			return invalid("a link in the source goes through another link: " + safe(name))
		}
	}
	return nil
}

// through reports whether dir or one of its ancestors is a link.
func through(dir string, links map[string]string) bool {
	for dir != "." && dir != "/" && dir != "" {
		if _, ok := links[dir]; ok {
			return true
		}
		dir = path.Dir(dir)
	}
	return false
}

// cleanName returns the entry's path relative to the folder ("" for the
// folder itself), refusing anything that could point outside it.
func cleanName(raw string) (string, error) {
	switch {
	case len(raw) > maxPath:
		return "", invalid("a path in the source is too long")
	case strings.ContainsAny(raw, "\\\x00"):
		return "", invalid("a path in the source has invalid characters: " + safe(raw))
	case strings.HasPrefix(raw, "/"):
		return "", invalid("a path in the source is absolute: " + safe(raw))
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", invalid("a path in the source points outside the folder: " + safe(raw))
		}
	}
	name := path.Clean(raw)
	if name == "." {
		return "", nil
	}
	return name, nil
}

// checkLink refuses symlinks whose target leaves the folder.
func checkLink(name, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\\x00") || len(target) > maxPath {
		return invalid("a link in the source points outside the folder: " + safe(name))
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return invalid("a link in the source points outside the folder: " + safe(name))
	}
	return nil
}

func safe(s string) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// sparse reports a sparse file in any of the GNU encodings (old GNU type 'S'
// or PAX GNU.sparse.* records), which Go's tar reader expands transparently.
func sparse(hdr *tar.Header) bool {
	if hdr.Typeflag == tar.TypeGNUSparse {
		return true
	}
	for k := range hdr.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return true
		}
	}
	return false
}

// budget fails reads past left bytes, remembering why.
type budget struct {
	r        io.Reader
	left     int64
	exceeded bool
}

func (b *budget) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One more byte would exceed: probe without consuming the budget.
		var one [1]byte
		n, err := b.r.Read(one[:])
		if n > 0 {
			b.exceeded = true
			return 0, errors.New("unpacked limit exceeded")
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}

// Extract verifies the archive in r and writes it under dir, which must
// exist and be empty. Files keep their permission bits (without setuid,
// setgid or sticky); links are created as they are (walk checked them).
func Extract(r io.Reader, dir string, limits Limits) (Summary, error) {
	v := NewVerifier(r, limits)
	defer v.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Summary{}, fmt.Errorf("open extraction folder: %w", err)
	}
	defer root.Close()
	var s Summary
	err = walk(v, limits, func(hdr *tar.Header, name string, body io.Reader) error {
		local := filepath.FromSlash(name)
		if parent := filepath.Dir(local); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("extract %s: %w", safe(name), err)
			}
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(local, 0o755); err != nil {
				return fmt.Errorf("extract %s: %w", safe(name), err)
			}
		case tar.TypeSymlink:
			if err := root.Symlink(hdr.Linkname, local); err != nil {
				return fmt.Errorf("extract %s: %w", safe(name), err)
			}
		case tar.TypeReg:
			s.Files++
			s.UnpackedSize += hdr.Size
			f, err := root.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return fmt.Errorf("extract %s: %w", safe(name), err)
			}
			_, err = io.Copy(f, body)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	// The verifier's own pass ends when its source does.
	if _, err := io.Copy(io.Discard, v); err != nil {
		return Summary{}, err
	}
	sum := v.Summary()
	sum.Files, sum.UnpackedSize = s.Files, s.UnpackedSize
	return sum, nil
}
