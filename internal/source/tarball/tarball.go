// Package tarball repacks the tarball GitHub serves for a commit
// (GET /repos/{owner}/{repo}/tarball/{sha}) into the clean .tar.gz the rest of
// the platform takes as a source: no pax global header, no top folder named
// after the repository and commit, and optionally only one folder of the
// repository, re-rooted. It streams: memory does not grow with the size of the
// files (only with the number of entries, which is bounded by the limits).
//
// The output is meant to pass pkg/srcarchive's verification, and the checks
// here mirror its rules so that what Repack accepts, srcarchive accepts.
package tarball

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

// ErrRootMissing is returned when a root was given but nothing lives under it.
var ErrRootMissing = errors.New("tarball: the folder does not exist in the repository")

// Summary is what Repack wrote.
type Summary struct {
	Files        int   // regular files
	UnpackedSize int64 // bytes of the regular files
}

// maxPath mirrors srcarchive's bound on a path.
const maxPath = 4096

// Repack reads GitHub's gzipped tarball from r and writes to w a gzipped tar
// holding only the files under root ("" is the whole repository; "apps/web"
// is that folder, re-rooted so its contents sit at the archive root). It
// removes the single top folder and the pax global headers, and enforces
// limits on the output (compressed size, unpacked bytes, entries) and the
// compressed size of the input.
//
// Errors: *srcarchive.TooLarge over a limit, ErrRootMissing when nothing
// lives under root, *srcarchive.Invalid for a malformed or unacceptable
// tarball. Errors writing to w are returned as they are; an error of r itself
// (anything but io.EOF) comes back wrapped and is never a srcarchive error, so
// the caller can retry it. The decompressed stream is bounded to 4× the
// unpacked limit, entries outside root included. After an error, w's contents
// are not meaningful.
func Repack(r io.Reader, w io.Writer, root string, limits srcarchive.Limits) (Summary, error) {
	var sum Summary
	if root != "" && (root != path.Clean(root) || root == "." || root == ".." ||
		strings.HasPrefix(root, "/") || strings.HasPrefix(root, "../") ||
		strings.ContainsAny(root, "\\\x00") || len(root) > maxPath) {
		return sum, invalid("the folder is not a valid relative path: " + safe(root))
	}

	in := &limitReader{r: r, left: limits.Compressed, limit: limits.Compressed}
	gz, err := gzip.NewReader(in)
	if err != nil {
		if e := in.failure(); e != nil {
			return sum, e
		}
		if e := in.readFailure(); e != nil {
			return sum, e
		}
		if !isFormatErr(err) {
			return sum, err
		}
		return sum, invalid("the repository tarball is not a gzip archive")
	}
	defer gz.Close()

	// Bound everything the tar reader consumes from the gzip stream, entries
	// skipped for being outside root included: the output limit does not cover
	// those, and a tiny gzip can expand to many GB. The app's folder may be a
	// small part of a monorepo, so the cap is a multiple of the unpacked limit
	// rather than the limit itself, but the whole expansion stays bounded.
	expandCap := int64(math.MaxInt64)
	if limits.Unpacked <= math.MaxInt64/expandFactor {
		expandCap = limits.Unpacked * expandFactor
	}
	dec := &expandReader{r: gz, left: expandCap, limit: expandCap}

	out := &limitWriter{w: w, left: limits.Compressed, limit: limits.Compressed}
	zw, err := gzip.NewWriterLevel(out, gzip.DefaultCompression)
	if err != nil {
		return sum, err
	}
	// srcarchive bounds the whole expanded tar stream (headers and padding
	// too), so the output is bounded the same way.
	tw := tar.NewWriter(&countWriter{w: zw, left: limits.Unpacked, limit: limits.Unpacked})

	// fail classifies an error from the read side.
	fail := func(err error) error {
		if e := in.failure(); e != nil {
			return e
		}
		// The download itself broke (not the content): retryable, not damaged.
		if e := in.readFailure(); e != nil {
			return e
		}
		if e := dec.failure(); e != nil {
			return e
		}
		var ie *srcarchive.Invalid
		var te *srcarchive.TooLarge
		var we *writeError
		switch {
		case errors.As(err, &we):
			return we.err
		case errors.As(err, &ie), errors.As(err, &te):
			return err
		case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, tar.ErrHeader),
			isFormatErr(err):
			return invalid("the repository tarball is damaged: " + safe(err.Error()))
		}
		return err
	}

	var (
		tr       = tar.NewReader(dec)
		top      string // the single top folder, once seen
		entries  int
		seen     = map[string]byte{}
		links    = map[string]string{} // symlink name → target
		names    []string
		rootPref = root + "/"
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, fail(err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if sparse(hdr) {
			return sum, invalid("the repository tarball holds a sparse file: " + safe(hdr.Name))
		}
		rel, err := stripTop(hdr.Name, &top)
		if err != nil {
			return sum, err
		}
		if rel == "" && top != "" && hdr.Typeflag != tar.TypeDir {
			// Only the top folder itself may be named by one component.
			return sum, invalid("the repository tarball has an entry outside its top folder: " + safe(hdr.Name))
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir, tar.TypeSymlink:
		case tar.TypeLink:
			// git never makes hard links; srcarchive refuses them.
			return sum, invalid("the repository tarball holds a hard link: " + safe(hdr.Name))
		default:
			return sum, invalid("the repository tarball holds an unsupported entry type: " + safe(hdr.Name))
		}

		var linkname string
		if hdr.Typeflag == tar.TypeSymlink {
			linkname = hdr.Linkname
		}

		// Re-root.
		name := rel
		switch {
		case root == "":
		case rel == root:
			name = ""
		case strings.HasPrefix(rel, rootPref):
			name = rel[len(rootPref):]
		default:
			continue // outside root (or an ancestor of it)
		}
		if name == "" {
			continue // the root folder itself (or a non-directory named like it)
		}
		if hdr.Typeflag == tar.TypeSymlink {
			if err := checkLink(name, linkname); err != nil {
				return sum, err
			}
			links[name] = linkname
		}

		entries++
		if entries > limits.Files {
			return sum, &srcarchive.TooLarge{Limit: int64(limits.Files)}
		}
		if prev, dup := seen[name]; dup && (prev != tar.TypeDir || hdr.Typeflag != tar.TypeDir) {
			return sum, invalid("the repository tarball has the same path twice: " + safe(name))
		}
		seen[name] = hdr.Typeflag
		names = append(names, name)

		oh := &tar.Header{
			Typeflag: hdr.Typeflag,
			Name:     name,
			Linkname: linkname,
			Mode:     hdr.Mode & 0o777,
			ModTime:  hdr.ModTime.Truncate(time.Second),
			Format:   tar.FormatPAX,
		}
		if hdr.Typeflag == tar.TypeDir {
			oh.Name += "/"
		}
		if hdr.Typeflag == tar.TypeReg {
			if hdr.Size < 0 || hdr.Size > limits.Unpacked-sum.UnpackedSize {
				return sum, &srcarchive.TooLarge{Limit: limits.Unpacked}
			}
			oh.Size = hdr.Size
		}
		if err := tw.WriteHeader(oh); err != nil {
			return sum, wrapWrite(out, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(&bodyWriter{tw}, tr); err != nil {
				return sum, fail(err)
			}
			sum.Files++
			sum.UnpackedSize += hdr.Size
		}
	}
	// Read to the end of the gzip stream: it checks the CRC and the size.
	if _, err := io.Copy(io.Discard, dec); err != nil {
		return sum, fail(err)
	}

	if entries == 0 {
		if root != "" {
			return sum, ErrRootMissing
		}
		if top == "" {
			return sum, invalid("the repository tarball is empty")
		}
		return sum, invalid("the repository is empty")
	}
	if err := checkThroughLinks(names, links); err != nil {
		return sum, err
	}
	if err := tw.Close(); err != nil {
		return sum, wrapWrite(out, err)
	}
	if err := zw.Close(); err != nil {
		return sum, wrapWrite(out, err)
	}
	return sum, nil
}

// stripTop validates a path from the tarball and returns it without its top
// folder ("" for the top folder itself). The first path seen fixes the top
// folder; a different one later is an error.
func stripTop(raw string, top *string) (string, error) {
	name, err := cleanName(raw)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", invalid("the repository tarball has an entry outside its top folder: " + safe(raw))
	}
	first, rest, _ := strings.Cut(name, "/")
	if *top == "" {
		*top = first
	} else if first != *top {
		return "", invalid("the repository tarball has more than one top folder: " + safe(raw))
	}
	return rest, nil
}

// cleanName mirrors srcarchive's rules for a path; "" means the folder itself.
func cleanName(raw string) (string, error) {
	switch {
	case len(raw) > maxPath:
		return "", invalid("a path in the repository tarball is too long")
	case strings.ContainsAny(raw, "\\\x00"):
		return "", invalid("a path in the repository tarball has invalid characters: " + safe(raw))
	case strings.HasPrefix(raw, "/"):
		return "", invalid("a path in the repository tarball is absolute: " + safe(raw))
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", invalid("a path in the repository tarball points outside the folder: " + safe(raw))
		}
	}
	name := path.Clean(raw)
	if name == "." {
		return "", nil
	}
	return name, nil
}

// checkLink refuses symlinks whose target leaves the (re-rooted) folder, with
// srcarchive's semantics.
func checkLink(name, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\\x00") || len(target) > maxPath {
		return invalid("a link in the repository tarball points outside the folder: " + safe(name))
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return invalid("a link in the repository tarball points outside the folder: " + safe(name))
	}
	return nil
}

// checkThroughLinks mirrors srcarchive's: only a path's last component may be
// a link.
func checkThroughLinks(names []string, links map[string]string) error {
	if len(links) == 0 {
		return nil
	}
	for _, name := range names {
		if through(path.Dir(name), links) {
			return invalid("a path in the repository tarball goes through a link: " + safe(name))
		}
	}
	for name, target := range links {
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
				return invalid("a link in the repository tarball goes through another link: " + safe(name))
			}
		}
		if through(path.Dir(cur), links) {
			return invalid("a link in the repository tarball goes through another link: " + safe(name))
		}
	}
	return nil
}

func through(dir string, links map[string]string) bool {
	for dir != "." && dir != "/" && dir != "" {
		if _, ok := links[dir]; ok {
			return true
		}
		dir = path.Dir(dir)
	}
	return false
}

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

func invalid(msg string) error { return &srcarchive.Invalid{Message: msg} }

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

func isFormatErr(err error) bool {
	return errors.Is(err, gzip.ErrHeader) || errors.Is(err, gzip.ErrChecksum) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		strings.Contains(err.Error(), "flate:") || strings.Contains(err.Error(), "archive/tar:")
}

// limitReader fails reads past left bytes, remembering it.
type limitReader struct {
	r        io.Reader
	left     int64
	limit    int64
	exceeded bool
	readErr  error // a non-EOF error from the source: the download broke
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, errors.New("compressed limit exceeded")
	}
	n, err := l.r.Read(p)
	if err != nil && err != io.EOF {
		l.readErr = err
	}
	l.left -= int64(n)
	if l.left < 0 {
		l.exceeded = true
		return 0, errors.New("compressed limit exceeded")
	}
	return n, err
}

func (l *limitReader) failure() error {
	if l.exceeded {
		return &srcarchive.TooLarge{Limit: l.limit}
	}
	return nil
}

// readFailure is the error to return when the source reader itself failed
// (network error, cut body, cancelled context). It is deliberately not a
// srcarchive error so the caller treats it as transient.
func (l *limitReader) readFailure() error {
	if l.readErr != nil {
		return fmt.Errorf("read the repository tarball: %w", l.readErr)
	}
	return nil
}

// expandFactor is how many times the unpacked limit the decompressed stream
// may be, to leave room for the parts of the repository outside root.
const expandFactor = 4

// expandReader bounds the bytes read out of the gzip stream.
type expandReader struct {
	r        io.Reader
	left     int64
	limit    int64
	exceeded bool
}

func (e *expandReader) Read(p []byte) (int, error) {
	if e.exceeded {
		return 0, errors.New("expanded limit exceeded")
	}
	n, err := e.r.Read(p)
	e.left -= int64(n)
	if e.left < 0 {
		e.exceeded = true
		return 0, errors.New("expanded limit exceeded")
	}
	return n, err
}

func (e *expandReader) failure() error {
	if e.exceeded {
		return &srcarchive.TooLarge{Limit: e.limit}
	}
	return nil
}

// limitWriter bounds the compressed output.
type limitWriter struct {
	w        io.Writer
	left     int64
	limit    int64
	exceeded bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.exceeded || int64(len(p)) > l.left {
		l.exceeded = true
		return 0, &srcarchive.TooLarge{Limit: l.limit}
	}
	n, err := l.w.Write(p)
	l.left -= int64(n)
	return n, err
}

// writeError tags an error of the output side so it is not taken for a
// problem of the input when io.Copy returns it.
type writeError struct{ err error }

func (e *writeError) Error() string { return e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

type bodyWriter struct{ tw *tar.Writer }

func (b *bodyWriter) Write(p []byte) (int, error) {
	n, err := b.tw.Write(p)
	if err != nil {
		return n, &writeError{err}
	}
	return n, nil
}

// countWriter bounds the uncompressed tar stream.
type countWriter struct {
	w     io.Writer
	left  int64
	limit int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, &srcarchive.TooLarge{Limit: c.limit}
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

func wrapWrite(out *limitWriter, err error) error {
	var te *srcarchive.TooLarge
	if errors.As(err, &te) {
		return te
	}
	if out.exceeded {
		return &srcarchive.TooLarge{Limit: out.limit}
	}
	return fmt.Errorf("tarball: write: %w", err)
}
