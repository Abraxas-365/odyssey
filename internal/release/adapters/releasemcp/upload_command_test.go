package releasemcp

// The upload command runs for real: the archive it streams must pass the
// same check a source gets (srcarchive), from a git repository and from a
// plain folder, with the secrets and dependencies left out. bsdtar's -z once
// padded a piped archive with zeros the check refuses (found in the 1d-2
// demo), which only running the command catches.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

func TestUploadCommandStreamsACheckedArchive(t *testing.T) {
	for _, bin := range []string{"bash", "tar", "gzip", "curl", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	for _, git := range []bool{false, true} {
		dir := t.TempDir()
		write := func(name, body string) {
			t.Helper()
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("server.js", "require('http').createServer((q, r) => r.end('ok')).listen(process.env.PORT)\n")
		write("package.json", `{"scripts":{"start":"node server.js"}}`+"\n")
		write("src/app.js", "module.exports = 1\n")
		write(".env.local", "SECRET=nope\n")
		write("node_modules/x/index.js", "dependency\n")
		want := []string{"package.json", "server.js", "src/app.js"}
		if git {
			write(".gitignore", ".env*\nnode_modules/\n")
			run(t, dir, "git", "init", "-q")
			run(t, dir, "git", "add", "package.json")
			want = append(want, ".gitignore")
		}

		var names []string
		var verdict error
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			v := srcarchive.NewVerifier(bytes.NewReader(body), srcarchive.Platform())
			if _, verdict = io.Copy(io.Discard, v); verdict == nil {
				names = entries(t, body)
			}
			w.WriteHeader(http.StatusAccepted)
		}))
		cmd := exec.Command("bash", "-c", uploadCommand(srv.URL+"/uploads/odu_test"))
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		srv.Close()
		if err != nil {
			t.Fatalf("git=%v: %v\n%s", git, err, out)
		}
		if verdict != nil {
			t.Fatalf("git=%v: the archive is refused: %v", git, verdict)
		}
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Fatalf("git=%v: archive holds %v, want %v", git, names, want)
		}
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// entries are the archive's regular files, cleaned and sorted.
func entries(t *testing.T, archive []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			names = append(names, filepath.Clean(hdr.Name))
		}
	}
	slices.Sort(names)
	return names
}
