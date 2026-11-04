package skills_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/skills"
)

func TestBuildIsDeterministic(t *testing.T) {
	a, err := skills.Build()
	if err != nil {
		t.Fatal(err)
	}
	b, err := skills.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Archive, b.Archive) || a.Version != b.Version || len(a.Version) != 12 {
		t.Fatalf("two builds differ: %s vs %s", a.Version, b.Version)
	}
	if d := skills.Default(); d.Version != a.Version {
		t.Fatalf("default %s, build %s", d.Version, a.Version)
	}
}

func TestArchiveLayout(t *testing.T) {
	b, err := skills.Build()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b.Archive))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	files := map[string]string{}
	for tr := tar.NewReader(zr); ; {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if !strings.HasPrefix(h.Name, "odicea/") && h.Name != "odicea/" {
			t.Errorf("entry %q outside odicea/", h.Name)
		}
		if h.ModTime.Unix() != 1577836800 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" {
			t.Errorf("%s: mtime %v owner %d:%d %q", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname)
		}
		if h.Typeflag == tar.TypeDir && h.Mode != 0o755 || h.Typeflag == tar.TypeReg && h.Mode != 0o644 {
			t.Errorf("%s: mode %o", h.Name, h.Mode)
		}
		if h.Typeflag == tar.TypeReg {
			data, _ := io.ReadAll(tr)
			files[h.Name] = string(data)
		}
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("entries not sorted: %q before %q", names[i-1], names[i])
		}
	}
	for _, want := range []string{"odicea/SKILL.md", "odicea/references/preparar-app.md", "odicea/references/diagnosticar.md", "odicea/references/github.md", "odicea/references/identidad.md"} {
		if files[want] == "" {
			t.Errorf("%s missing from the archive", want)
		}
	}
}

func TestSkillFrontmatter(t *testing.T) {
	raw, err := skills.FS.ReadFile("odicea/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	head, _, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
	if !strings.HasPrefix(text, "---\n") || !ok {
		t.Fatal("SKILL.md has no frontmatter")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(head, "\n") {
		k, v, found := strings.Cut(line, ": ")
		if !found {
			t.Fatalf("frontmatter line %q is not a one-line `key: value`", line)
		}
		fields[k] = v
	}
	if fields["name"] != skills.Name || len(fields) != 2 {
		t.Fatalf("frontmatter %v: want name %q and description only", fields, skills.Name)
	}
	d := fields["description"]
	if d == "" || len(d) > 1024 {
		t.Fatalf("description is %d bytes", len(d))
	}
	if !strings.Contains(d, "Odicea") {
		t.Errorf("description does not name Odicea: %q", d)
	}
}

func TestSkillLinksResolve(t *testing.T) {
	raw, _ := skills.FS.ReadFile("odicea/SKILL.md")
	for _, m := range regexp.MustCompile(`\]\((references/[^)#]+)\)`).FindAllStringSubmatch(string(raw), -1) {
		if _, err := skills.FS.ReadFile("odicea/" + m[1]); err != nil {
			t.Errorf("SKILL.md links %s: %v", m[1], err)
		}
	}
}
