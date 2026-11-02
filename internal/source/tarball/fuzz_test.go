package tarball

import (
	"bytes"
	"io"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

func FuzzRepack(f *testing.F) {
	top := "o-r-abc/"
	seeds := [][]ent{
		{dir(top), file(top+"a.txt", "a")},
		{dir(top), file(top+"apps/web/main.go", "package main"), sym(top+"apps/web/l", "main.go"), file(top+"apps/webby/x", "x")},
		{file(top+"a/b/c", "c"), sym(top+"a/up", "../b/c"), sym(top+"a/esc", "../../x")},
		{file(top+"f", "f"), hard(top+"h", top+"f")},
		{file("a/x", "1"), file("b/y", "2")},
	}
	roots := []string{"", "apps/web", "a", "a/b", "nope"}
	for i, s := range seeds {
		f.Add(build(f, i%2 == 0, s...), roots[i])
	}
	f.Add([]byte("not gzip"), "")
	f.Add(build(f, true, file(top+"../x", "x")), "")

	limits := srcarchive.Limits{Compressed: 1 << 20, Unpacked: 1 << 20, Files: 1000}
	f.Fuzz(func(t *testing.T, data []byte, root string) {
		var out bytes.Buffer
		sum, err := Repack(bytes.NewReader(data), &out, root, limits)
		if err != nil {
			return
		}
		v := srcarchive.NewVerifier(bytes.NewReader(out.Bytes()), limits)
		defer v.Close()
		if _, err := io.Copy(io.Discard, v); err != nil {
			t.Fatalf("Repack succeeded but srcarchive rejects: %v", err)
		}
		s := v.Summary()
		if s.Files != sum.Files || s.UnpackedSize != sum.UnpackedSize {
			t.Fatalf("summary mismatch: srcarchive %d/%d, Repack %+v", s.Files, s.UnpackedSize, sum)
		}
	})
}
