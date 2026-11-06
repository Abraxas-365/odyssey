package srcarchive_test

import (
	"bytes"
	"strings"
	"testing"

	archive "github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

// The content digest is the files', not the archive's: another order,
// other folders, times or permission bits leave it alone; any change to a
// file's bytes, path, executable bit or a link's target changes it. A
// folder cut out of a monorepo has the digest of the same files uploaded
// on their own.
func TestContentDigest(t *testing.T) {
	digest := func(t *testing.T, entries ...entry) string {
		t.Helper()
		_, sum, err := verify(tgz(t, entries...), limits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(sum.ContentDigest, archive.ContentVersion+":") || len(sum.ContentDigest) != len(archive.ContentVersion)+1+64 {
			t.Fatalf("digest %q", sum.ContentDigest)
		}
		return sum.ContentDigest
	}
	exec := func(name, body string) entry { e := file(name, body); e.mode = 0o755; return e }
	base := digest(t, file("a.txt", "1"), dir("src"), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3"))

	same := map[string][]entry{
		"other order":         {link("latest", "src"), exec("run.sh", "3"), file("src/b.go", "2"), file("a.txt", "1")},
		"other non-exec mode": {func() entry { e := file("a.txt", "1"); e.mode = 0o600; return e }(), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3")},
		"other exec mode":     {file("a.txt", "1"), file("src/b.go", "2"), link("latest", "src"), func() entry { e := file("run.sh", "3"); e.mode = 0o700; return e }()},
		"extra folder":        {file("a.txt", "1"), dir("src"), dir("empty"), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3")},
		"dot-slash names":     {file("./a.txt", "1"), file("./src/b.go", "2"), link("./latest", "src"), exec("./run.sh", "3")},
	}
	for name, entries := range same {
		if got := digest(t, entries...); got != base {
			t.Errorf("%s changed the digest", name)
		}
	}
	differ := map[string][]entry{
		"a byte":        {file("a.txt", "X"), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3")},
		"a path":        {file("A.txt", "1"), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3")},
		"the exec bit":  {file("a.txt", "1"), file("src/b.go", "2"), link("latest", "src"), file("run.sh", "3")},
		"a link target": {file("a.txt", "1"), file("src/b.go", "2"), link("latest", "."), exec("run.sh", "3")},
		"a file more":   {file("a.txt", "1"), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3"), file("c", "")},
		"a file less":   {file("a.txt", "1"), file("src/b.go", "2"), link("latest", "src")},
		// a newline in a name cannot forge another entry's line
		"a forged name": {file("a.txt\"\nf \"x", "1"), file("src/b.go", "2"), link("latest", "src"), exec("run.sh", "3")},
	}
	for name, entries := range differ {
		if got := digest(t, entries...); got == base {
			t.Errorf("%s did not change the digest", name)
		}
	}

	// A folder of a monorepo is digested as the same files on their own.
	mono := tgz(t, file("README", "x"), dir("apps/web"), file("apps/web/a.txt", "1"), file("apps/web/src/b.go", "2"),
		link("apps/web/latest", "src"), exec("apps/web/run.sh", "3"), file("apps/api/main.go", "4"))
	var out bytes.Buffer
	sum, err := archive.Subtree(bytes.NewReader(mono), &out, "apps/web", limits)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ContentDigest != base {
		t.Fatalf("the cut folder's digest %s, alone %s", sum.ContentDigest, base)
	}
	// and the archive Subtree wrote verifies to the same digest
	if _, again, err := verify(out.Bytes(), limits); err != nil || again.ContentDigest != base {
		t.Fatalf("re-verified %s %v", again.ContentDigest, err)
	}
}
