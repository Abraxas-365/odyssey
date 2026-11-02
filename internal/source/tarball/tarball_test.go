package tarball

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

type ent struct {
	name     string
	typeflag byte
	body     string
	link     string
	mode     int64
}

func file(name, body string) ent {
	return ent{name: name, typeflag: tar.TypeReg, body: body, mode: 0o644}
}
func dir(name string) ent { return ent{name: name, typeflag: tar.TypeDir, mode: 0o755} }
func sym(name, to string) ent {
	return ent{name: name, typeflag: tar.TypeSymlink, link: to, mode: 0o777}
}
func hard(name, to string) ent { return ent{name: name, typeflag: tar.TypeLink, link: to, mode: 0o644} }

// build makes a GitHub-like tarball: a pax global header then the entries.
func build(t testing.TB, withGlobal bool, ents ...ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if withGlobal {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
			PAXRecords: map[string]string{"comment": "0123456789abcdef0123456789abcdef01234567"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range ents {
		h := &tar.Header{
			Typeflag: e.typeflag, Name: e.name, Linkname: e.link, Mode: e.mode,
			Uid: 1000, Gid: 1000, Uname: "u", Gname: "g",
			ModTime: time.Unix(1700000000, 0), Size: int64(len(e.body)),
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
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

type outEnt struct {
	Name, Link string
	Type       byte
	Body       string
	Mode       int64
}

func read(t testing.TB, data []byte) []outEnt {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []outEnt
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: owner not cleared: %d %d %q %q", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if len(h.PAXRecords) != 0 && !(len(h.PAXRecords) == 1 && h.PAXRecords["path"] != "") {
			for k := range h.PAXRecords {
				if k != "path" && k != "linkpath" {
					t.Errorf("%s: extra PAX record %q", h.Name, k)
				}
			}
		}
		out = append(out, outEnt{h.Name, h.Linkname, h.Typeflag, string(b), h.Mode})
	}
}

func names(es []outEnt) []string {
	var n []string
	for _, e := range es {
		n = append(n, e.Name)
	}
	return n
}

func verify(t testing.TB, data []byte, want Summary) {
	t.Helper()
	v := srcarchive.NewVerifier(bytes.NewReader(data), srcarchive.Platform())
	if _, err := io.Copy(io.Discard, v); err != nil {
		t.Fatalf("srcarchive rejects output: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	s := v.Summary()
	if s.Files != want.Files || s.UnpackedSize != want.UnpackedSize {
		t.Errorf("srcarchive summary = %d files %d bytes, Repack said %+v", s.Files, s.UnpackedSize, want)
	}
}

func repack(in []byte, root string, l srcarchive.Limits) ([]byte, Summary, error) {
	var out bytes.Buffer
	s, err := Repack(bytes.NewReader(in), &out, root, l)
	return out.Bytes(), s, err
}

func TestRepack(t *testing.T) {
	const top = "acme-app-abc1234/"
	repo := []ent{
		dir(top),
		file(top+"README.md", "hello"),
		dir(top + "apps/"),
		dir(top + "apps/web/"),
		file(top+"apps/web/main.go", "package main"),
		{name: top + "apps/web/run.sh", typeflag: tar.TypeReg, body: "#!/bin/sh", mode: 0o755},
		file(top+"apps/web/sub/x.txt", "x"),
		file(top+"apps/webby/y.txt", "yy"),
		sym(top+"apps/web/link", "main.go"),
	}

	tests := []struct {
		name      string
		ents      []ent
		global    bool
		root      string
		want      []string
		summary   Summary
		checkMode map[string]int64
	}{
		{
			name: "whole repo", ents: repo, global: true,
			want:      []string{"README.md", "apps/", "apps/web/", "apps/web/main.go", "apps/web/run.sh", "apps/web/sub/x.txt", "apps/webby/y.txt", "apps/web/link"},
			summary:   Summary{Files: 5, UnpackedSize: int64(len("hello") + len("package main") + len("#!/bin/sh") + 1 + 2)},
			checkMode: map[string]int64{"apps/web/run.sh": 0o755, "README.md": 0o644},
		},
		{
			name: "subfolder re-rooted", ents: repo, global: true, root: "apps/web",
			want:    []string{"main.go", "run.sh", "sub/x.txt", "link"},
			summary: Summary{Files: 3, UnpackedSize: int64(len("package main") + len("#!/bin/sh") + 1)},
		},
		{
			name: "prefix sibling not matched", ents: repo, root: "apps/we",
			want: nil,
		},
		{
			name: "no pax global header", ents: repo, root: "apps/webby",
			want: []string{"y.txt"}, summary: Summary{Files: 1, UnpackedSize: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := build(t, tc.global, tc.ents...)
			out, s, err := repack(data, tc.root, srcarchive.Platform())
			if tc.want == nil {
				if !errors.Is(err, ErrRootMissing) {
					t.Fatalf("err = %v, want ErrRootMissing", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s != tc.summary {
				t.Errorf("summary = %+v, want %+v", s, tc.summary)
			}
			es := read(t, out)
			got := names(es)
			for i, n := range got {
				got[i] = strings.TrimSuffix(n, "/")
			}
			want := make([]string, len(tc.want))
			for i, n := range tc.want {
				want[i] = strings.TrimSuffix(n, "/")
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("entries = %v, want %v", got, want)
			}
			for _, e := range es {
				if strings.Contains(e.Name, "pax_global_header") || strings.HasPrefix(e.Name, "acme-app") {
					t.Errorf("leaked %q", e.Name)
				}
				if m, ok := tc.checkMode[strings.TrimSuffix(e.Name, "/")]; ok && e.Mode != m {
					t.Errorf("%s mode = %o, want %o", e.Name, e.Mode, m)
				}
				switch e.Name {
				case "link":
					if e.Type != tar.TypeSymlink || e.Link != "main.go" {
						t.Errorf("link = %+v", e)
					}
				}
			}
			verify(t, out, s)
		})
	}
}

func TestRepackContent(t *testing.T) {
	data := build(t, true, dir("o-r-1/"), file("o-r-1/a/b.txt", "contents"))
	out, _, err := repack(data, "a", srcarchive.Platform())
	if err != nil {
		t.Fatal(err)
	}
	es := read(t, out)
	if len(es) != 1 || es[0].Name != "b.txt" || es[0].Body != "contents" {
		t.Fatalf("got %+v", es)
	}
}

func TestRepackSymlinkInsideRoot(t *testing.T) {
	// Escapes the repo's folder but stays inside the repository: fine at
	// whole-repo level, not once re-rooted.
	ents := []ent{
		dir("o-r-1/"),
		file("o-r-1/secret", "s"),
		file("o-r-1/apps/web/a", "a"),
		sym("o-r-1/apps/web/up", "../../secret"),
		sym("o-r-1/apps/web/in", "../web/a"),
	}
	data := build(t, true, ents...)
	if out, s, err := repack(data, "", srcarchive.Platform()); err != nil {
		t.Fatalf("whole repo: %v", err)
	} else {
		verify(t, out, s)
	}
	_, _, err := repack(data, "apps/web", srcarchive.Platform())
	var inv *srcarchive.Invalid
	if !errors.As(err, &inv) || !strings.Contains(inv.Message, "apps/web") && !strings.Contains(inv.Message, "up") {
		t.Fatalf("err = %v, want Invalid naming the link", err)
	}
}

func TestRepackInvalid(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		root string
	}{
		{"not gzip", []byte("this is not a gzip stream at all"), ""},
		{"empty input", nil, ""},
		{"gzip of non-tar", gz(t, []byte(strings.Repeat("junk", 300))), ""},
		{"two top folders", build(t, true, file("a-b-1/x", "x"), file("c-d-2/y", "y")), ""},
		{"dotdot", build(t, true, file("a-b-1/../../etc/passwd", "x")), ""},
		{"dotdot at end", build(t, true, file("a-b-1/x/..", "x")), ""},
		{"absolute", build(t, true, file("/etc/passwd", "x")), ""},
		{"file at top level", build(t, true, file("README", "x")), ""},
		{"absolute symlink", build(t, true, dir("a-b-1/"), sym("a-b-1/l", "/etc/passwd")), ""},
		{"symlink above repo", build(t, true, dir("a-b-1/"), sym("a-b-1/l", "../../x")), ""},
		{"hardlink", build(t, true, file("a-b-1/x/f", "f"), hard("a-b-1/x/h", "a-b-1/x/f")), "x"},
		{"hardlink to other top", build(t, true, file("a-b-1/f", "f"), hard("a-b-1/h", "c-d-2/f")), ""},
		{"fifo", build(t, true, dir("a-b-1/"), ent{name: "a-b-1/p", typeflag: tar.TypeFifo, mode: 0o644}), ""},
		{"char device", build(t, true, dir("a-b-1/"), ent{name: "a-b-1/c", typeflag: tar.TypeChar, mode: 0o644}), ""},
		{"duplicate file", build(t, true, file("a-b-1/x", "1"), file("a-b-1/x", "2")), ""},
		{"through link", build(t, true, dir("a-b-1/"), sym("a-b-1/l", "."), file("a-b-1/l/x", "x")), ""},
		{"truncated", func() []byte { b := build(t, true, file("a-b-1/x", strings.Repeat("z", 5000))); return b[:len(b)/2] }(), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := repack(tc.data, tc.root, srcarchive.Platform())
			var inv *srcarchive.Invalid
			if !errors.As(err, &inv) {
				t.Fatalf("err = %#v, want *srcarchive.Invalid", err)
			}
		})
	}
}

func gz(t testing.TB, b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestRepackLimits(t *testing.T) {
	var many []ent
	many = append(many, dir("a-b-1/"))
	for i := 0; i < 10; i++ {
		many = append(many, file("a-b-1/f"+string(rune('a'+i)), "x"))
	}
	t.Run("too many files", func(t *testing.T) {
		l := srcarchive.Platform()
		l.Files = 5
		_, _, err := repack(build(t, true, many...), "", l)
		var tl *srcarchive.TooLarge
		if !errors.As(err, &tl) {
			t.Fatalf("err = %v, want TooLarge", err)
		}
	})
	t.Run("files only counted under root", func(t *testing.T) {
		l := srcarchive.Platform()
		l.Files = 2
		in := build(t, true, file("a-b-1/in/1", "1"), file("a-b-1/in/2", "2"), file("a-b-1/out/1", "1"), file("a-b-1/out/2", "2"))
		if _, _, err := repack(in, "in", l); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unpacked too large", func(t *testing.T) {
		l := srcarchive.Platform()
		l.Unpacked = 10 << 10
		_, _, err := repack(build(t, true, file("a-b-1/big", strings.Repeat("a", 64<<10))), "", l)
		var tl *srcarchive.TooLarge
		if !errors.As(err, &tl) {
			t.Fatalf("err = %v, want TooLarge", err)
		}
	})
	t.Run("many small files exceed unpacked via headers", func(t *testing.T) {
		l := srcarchive.Platform()
		l.Unpacked = 4 << 10
		_, _, err := repack(build(t, true, many...), "", l)
		var tl *srcarchive.TooLarge
		if !errors.As(err, &tl) {
			t.Fatalf("err = %v, want TooLarge", err)
		}
	})
	t.Run("compressed input too large", func(t *testing.T) {
		// Random-ish data so the gzip is big.
		var b strings.Builder
		x := uint32(1)
		for i := 0; i < 200000; i++ {
			x = x*1664525 + 1013904223
			b.WriteByte(byte(x >> 24))
		}
		l := srcarchive.Platform()
		l.Compressed = 10 << 10
		_, _, err := repack(build(t, true, file("a-b-1/rnd", b.String())), "", l)
		var tl *srcarchive.TooLarge
		if !errors.As(err, &tl) || tl.Limit != l.Compressed {
			t.Fatalf("err = %v, want TooLarge", err)
		}
	})
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRepackSourceReadError(t *testing.T) {
	data := build(t, true, file("a-b-1/x", strings.Repeat("z", 5000)))
	for _, cut := range []int{0, 5, len(data) / 2} {
		for _, srcErr := range []error{errors.New("connection reset"), io.ErrUnexpectedEOF, context.Canceled} {
			r := io.MultiReader(bytes.NewReader(data[:cut]), errReader{srcErr})
			_, err := Repack(r, io.Discard, "", srcarchive.Platform())
			var inv *srcarchive.Invalid
			var tl *srcarchive.TooLarge
			if err == nil || errors.As(err, &inv) || errors.As(err, &tl) || !errors.Is(err, srcErr) {
				t.Errorf("cut %d, source err %v: got %#v, want plain error wrapping it", cut, srcErr, err)
			}
		}
	}
}

func TestRepackCleanTruncationStaysInvalid(t *testing.T) {
	data := build(t, true, file("a-b-1/x", strings.Repeat("z", 5000)))
	for _, cut := range []int{5, len(data) / 2, len(data) - 3} {
		_, err := Repack(bytes.NewReader(data[:cut]), io.Discard, "", srcarchive.Platform())
		var inv *srcarchive.Invalid
		if !errors.As(err, &inv) {
			t.Errorf("cut %d: err = %#v, want *srcarchive.Invalid", cut, err)
		}
	}
}

func TestRepackSkippedContentBounded(t *testing.T) {
	l := srcarchive.Platform()
	l.Unpacked = 4 << 10 // cap on the decompressed stream: 16 KiB
	// 256 KiB of zeros outside root compresses to almost nothing.
	in := build(t, true, file("a-b-1/in/1", "1"), file("a-b-1/out/big", strings.Repeat("\x00", 256<<10)))
	_, _, err := repack(in, "in", l)
	var tl *srcarchive.TooLarge
	if !errors.As(err, &tl) || tl.Limit != 4*l.Unpacked {
		t.Fatalf("err = %#v, want TooLarge{Limit: %d}", err, 4*l.Unpacked)
	}
	// Within the 4x allowance the skipped content is fine.
	l.Unpacked = 128 << 10
	if _, _, err := repack(in, "in", l); err != nil {
		t.Fatalf("within allowance: %v", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRepackWriteError(t *testing.T) {
	data := build(t, true, file("a-b-1/x", strings.Repeat("q", 100000)))
	_, err := Repack(bytes.NewReader(data), failWriter{}, "", srcarchive.Platform())
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the write error", err)
	}
	var inv *srcarchive.Invalid
	if errors.As(err, &inv) {
		t.Fatalf("write error reported as Invalid")
	}
}

func TestRepackBadRoot(t *testing.T) {
	data := build(t, true, file("a-b-1/x", "x"))
	for _, root := range []string{"..", "../x", "/abs", "a/../b", "a/"} {
		if _, _, err := repack(data, root, srcarchive.Platform()); err == nil {
			t.Errorf("root %q accepted", root)
		}
	}
}
