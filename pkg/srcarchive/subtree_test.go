package srcarchive_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	archive "github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

func TestSubtreeKeepsOnlyTheFolderReRooted(t *testing.T) {
	data := tgz(t, file("package.json", "{}"), dir("apps"), dir("apps/web"), file("apps/web/package.json", `{"name":"web"}`),
		dir("apps/web/src"), file("apps/web/src/index.js", "1"), link("apps/web/latest", "src"),
		file("apps/webby/x", "not web"), file("apps/api/main.go", "package main"))
	var out bytes.Buffer
	sum, err := archive.Subtree(bytes.NewReader(data), &out, "apps/web", limits)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(out.Bytes())
	if sum.Files != 2 || sum.UnpackedSize != int64(len(`{"name":"web"}`)+1) || sum.Size != int64(out.Len()) || sum.SHA256 != hex.EncodeToString(h[:]) {
		t.Fatalf("summary %+v (archive of %d bytes)", sum, out.Len())
	}
	// What it wrote is a source by the same rules, holding only the folder.
	dir := t.TempDir()
	if _, err := archive.Extract(bytes.NewReader(out.Bytes()), dir, limits); err != nil {
		t.Fatalf("the folder's archive is not a valid source: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err != nil || string(b) != `{"name":"web"}` {
		t.Fatalf("package.json %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "index.js")); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"apps", "x", "main.go"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Fatalf("%s from outside the folder was kept", gone)
		}
	}
	if target, err := os.Readlink(filepath.Join(dir, "latest")); err != nil || target != "src" {
		t.Fatalf("link %q %v", target, err)
	}
}

func TestSubtreeRefuses(t *testing.T) {
	var invalid *archive.Invalid
	cases := map[string]struct {
		data []byte
		root string
		want func(error) bool
	}{
		"missing folder":               {tgz(t, file("apps/api/main.go", "x")), "apps/web", func(err error) bool { return errors.Is(err, archive.ErrRootMissing) }},
		"a file named like the folder": {tgz(t, file("apps/web", "x")), "apps/web", func(err error) bool { return errors.Is(err, archive.ErrRootMissing) }},
		"link out of the folder": {tgz(t, file("secret", "x"), link("apps/web/s", "../../secret")), "apps/web",
			func(err error) bool { return errors.As(err, &invalid) }},
		"bad root":       {tgz(t, file("a/b", "x")), "../a", func(err error) bool { return errors.As(err, &invalid) }},
		"empty root":     {tgz(t, file("a/b", "x")), "", func(err error) bool { return errors.As(err, &invalid) }},
		"unclean root":   {tgz(t, file("a/b", "x")), "a/./b", func(err error) bool { return errors.As(err, &invalid) }},
		"bad whole":      {tgz(t, file("apps/web/a", "x"), file("../escape", "x")), "apps/web", func(err error) bool { return errors.As(err, &invalid) }},
		"not an archive": {[]byte("plain"), "apps/web", func(err error) bool { return errors.As(err, &invalid) }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := archive.Subtree(bytes.NewReader(c.data), &bytes.Buffer{}, c.root, limits)
			if !c.want(err) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestSubtreeBoundsWhatItWrites(t *testing.T) {
	data := tgz(t, file("apps/web/big", randomish(64<<10)))
	if _, err := archive.Subtree(bytes.NewReader(data), &bytes.Buffer{}, "apps/web", limits); err != nil {
		t.Fatal(err)
	}
	// The archive it writes is held to the compressed limit too.
	var big *archive.TooLarge
	small := archive.Limits{Compressed: 2 << 10, Unpacked: limits.Unpacked, Files: limits.Files}
	if _, err := archive.Subtree(bytes.NewReader(data), &bytes.Buffer{}, "apps/web", small); !errors.As(err, &big) {
		t.Fatalf("an archive over the limit: %v", err)
	}
}
