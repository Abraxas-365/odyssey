package archive_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/build/archive"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func file(name, body string) entry {
	return entry{name: name, body: body, typ: tar.TypeReg, mode: 0o644}
}
func dir(name string) entry { return entry{name: name, typ: tar.TypeDir, mode: 0o755} }
func link(name, to string) entry {
	return entry{name: name, link: to, typ: tar.TypeSymlink, mode: 0o777}
}

func tgz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body)), Format: tar.FormatPAX}
		if e.typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil && e.typ == tar.TypeReg {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var limits = archive.Limits{Compressed: 1 << 20, Unpacked: 4 << 20, Files: 50}

// verify passes data through a Verifier like an upload does, returning what
// a consumer stored, the verdict and the summary.
func verify(data []byte, l archive.Limits) ([]byte, archive.Summary, error) {
	v := archive.NewVerifier(bytes.NewReader(data), l)
	defer v.Close()
	out, err := io.ReadAll(v)
	return out, v.Summary(), err
}

func TestVerifierAcceptsAFolder(t *testing.T) {
	data := tgz(t, dir("./"), file("go.mod", "module x\n"), dir("cmd"), file("cmd/main.go", "package main\n"),
		link("cmd/current", "../go.mod"), link("latest", "cmd"))
	out, sum, err := verify(data, limits)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("the verifier changed the bytes")
	}
	want := sha256.Sum256(data)
	if sum.SHA256 != hex.EncodeToString(want[:]) || sum.Size != int64(len(data)) || sum.Files != 2 ||
		sum.UnpackedSize != int64(len("module x\n")+len("package main\n")) {
		t.Fatalf("summary %+v", sum)
	}
}

func TestVerifierRefuses(t *testing.T) {
	big := strings.Repeat("a", 5<<20) // compresses to a few KiB: a decompression bomb
	many := []entry{}
	for i := range 51 {
		many = append(many, file("f"+strings.Repeat("x", i), "1"))
	}
	for name, data := range map[string][]byte{
		"not gzip":              []byte("hello, this is not an archive"),
		"gzip but not tar":      gzipOf(t, []byte(strings.Repeat("not a tar header ", 100))),
		"empty":                 tgz(t),
		"absolute path":         tgz(t, file("/etc/passwd", "x")),
		"parent path":           tgz(t, file("../escape", "x")),
		"nested parent":         tgz(t, file("a/../../escape", "x")),
		"backslash":             tgz(t, file(`a\..\escape`, "x")),
		"absolute link":         tgz(t, link("l", "/etc/passwd")),
		"escaping link":         tgz(t, link("a/l", "../../etc")),
		"through a link":        tgz(t, link("l", "."), file("l/x", "x")),
		"link through a link":   tgz(t, dir("sub"), link("l", "sub"), link("sub/up", ".."), link("a", "l/up/..")),
		"device":                tgz(t, entry{name: "dev", typ: tar.TypeChar, mode: 0o644}),
		"fifo":                  tgz(t, entry{name: "fifo", typ: tar.TypeFifo, mode: 0o644}),
		"hard link":             tgz(t, file("a", "x"), entry{name: "b", link: "a", typ: tar.TypeLink, mode: 0o644}),
		"duplicate file":        tgz(t, file("a", "x"), file("a", "y")),
		"file replaced by link": tgz(t, file("a", "x"), link("a", "b")),
		"decompression bomb":    tgz(t, file("big", big)),
		"too many files":        tgz(t, many...),
		"truncated":             tgz(t, file("a", strings.Repeat("x", 4096)))[:60],
		"sparse file":           gzipOf(t, sparseTar(1<<40)), // 3.5 KB of tar claiming a terabyte
		"data after the end":    append(tgz(t, file("a", "x")), gzipOf(t, []byte("smuggled"))...),
		"junk after the end":    append(tgz(t, file("a", "x")), "junk"...),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := verify(data, limits)
			var e *errx.Error
			if !errors.As(err, &e) || e.Code != archive.CodeInvalid {
				t.Fatalf("want %s, got %v", archive.CodeInvalid, err)
			}
			// Every later read repeats the verdict (a consumer never sees EOF).
			v := archive.NewVerifier(bytes.NewReader(data), limits)
			defer v.Close()
			_, _ = io.ReadAll(v)
			if _, err := v.Read(make([]byte, 8)); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("read after the verdict: %v", err)
			}
		})
	}
}

func TestVerifierCompressedLimit(t *testing.T) {
	data := tgz(t, file("a", randomish(64<<10)))
	_, _, err := verify(data, archive.Limits{Compressed: 1024, Unpacked: 1 << 20, Files: 10})
	var e *errx.Error
	if !errors.As(err, &e) || e.HTTPStatus != 413 {
		t.Fatalf("want 413, got %v", err)
	}
}

func TestVerifierPassesReaderErrors(t *testing.T) {
	cut := errors.New("connection reset")
	data := tgz(t, file("a", randomish(64<<10)))
	v := archive.NewVerifier(io.MultiReader(bytes.NewReader(data[:len(data)/2]), errReader{cut}), limits)
	defer v.Close()
	if _, err := io.ReadAll(v); !errors.Is(err, cut) {
		t.Fatalf("want the reader's error, got %v", err)
	}
}

func TestExtract(t *testing.T) {
	out := t.TempDir()
	data := tgz(t, file("go.mod", "module x\n"), dir("cmd"), entry{name: "cmd/run.sh", body: "#!/bin/sh\n", typ: tar.TypeReg, mode: 0o4755},
		link("latest", "cmd"))
	sum, err := archive.Extract(bytes.NewReader(data), out, limits)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 2 {
		t.Fatalf("summary %+v", sum)
	}
	b, err := os.ReadFile(filepath.Join(out, "go.mod"))
	if err != nil || string(b) != "module x\n" {
		t.Fatalf("go.mod %q %v", b, err)
	}
	info, err := os.Stat(filepath.Join(out, "cmd", "run.sh"))
	if err != nil || info.Mode()&os.ModeSetuid != 0 || info.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh mode %v %v", info.Mode(), err)
	}
	if target, err := os.Readlink(filepath.Join(out, "latest")); err != nil || target != "cmd" {
		t.Fatalf("link %q %v", target, err)
	}

	bad := t.TempDir()
	if _, err := archive.Extract(bytes.NewReader(tgz(t, file("../escape", "x"))), bad, limits); err == nil {
		t.Fatal("extracted an escaping path")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(bad), "escape")); err == nil {
		t.Fatal("a file escaped the folder")
	}
}

func gzipOf(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sparseTar is a PAX 1.0 sparse file (written by hand: Go's tar.Writer drops
// GNU.sparse.* records) of one data byte that claims realSize bytes, which
// Go's tar.Reader would expand as zeros.
func sparseTar(realSize int64) []byte {
	block := func(name string, size int64, typ byte) []byte {
		h := make([]byte, 512)
		copy(h, name)
		copy(h[100:], "0000644\x00")
		copy(h[108:], "0000000\x00")
		copy(h[116:], "0000000\x00")
		copy(h[124:], fmt.Sprintf("%011o\x00", size))
		copy(h[136:], "00000000000\x00")
		copy(h[148:], "        ")
		h[156] = typ
		copy(h[257:], "ustar\x0000")
		sum := 0
		for _, b := range h {
			sum += int(b)
		}
		copy(h[148:], fmt.Sprintf("%06o\x00 ", sum))
		return h
	}
	rec := func(k, v string) string {
		body := " " + k + "=" + v + "\n"
		n := len(body) + 1
		for len(strconv.Itoa(n))+len(body) != n {
			n = len(strconv.Itoa(n)) + len(body)
		}
		return strconv.Itoa(n) + body
	}
	pad := func(b []byte) []byte { return append(b, make([]byte, (512-len(b)%512)%512)...) }
	pax := rec("GNU.sparse.major", "1") + rec("GNU.sparse.minor", "0") + rec("GNU.sparse.name", "big") +
		rec("GNU.sparse.realsize", strconv.FormatInt(realSize, 10))
	data := append(pad([]byte("1\n0\n1\n")), 'x') // map: one region at 0, 1 byte long
	var buf bytes.Buffer
	buf.Write(block("PaxHeaders/big", int64(len(pax)), tar.TypeXHeader))
	buf.Write(pad([]byte(pax)))
	buf.Write(block("GNUSparseFile.0/big", int64(len(data)), tar.TypeReg))
	buf.Write(pad(data))
	buf.Write(make([]byte, 1024))
	return buf.Bytes()
}
func randomish(n int) string {
	b := make([]byte, n)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte('!' + x%90)
	}
	return string(b)
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
