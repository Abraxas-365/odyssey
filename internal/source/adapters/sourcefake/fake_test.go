package sourcefake_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcefake"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func arranged() *sourcefake.Fake {
	f := sourcefake.New(sourcefake.Options{WebhookSecret: "s3cret"})
	f.AddInstallation(source.InstallationInfo{ID: 7, Account: "acme", AccountType: "Organization"}, "code-1")
	f.AddRepo(7, source.Repo{ID: 42, FullName: "acme/web", DefaultBranch: "main"},
		map[string]source.Commit{"main": {SHA: sha, Title: "first", Author: "ana"}})
	return f
}

func TestTarballIsGitHubShaped(t *testing.T) {
	f := arranged()
	f.SetFiles(42, sha, map[string]string{"Dockerfile": "FROM scratch\n", "src/main.go": "package main\n"})
	body, err := f.Tarball(context.Background(), 7, 42, sha)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	gz, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var names []string
	contents := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			contents[h.Name] = string(b)
		}
		if h.Typeflag == tar.TypeXGlobalHeader && h.PAXRecords["comment"] != sha {
			t.Errorf("pax comment = %q", h.PAXRecords["comment"])
		}
	}
	if len(names) == 0 || names[0] != "pax_global_header" {
		t.Fatalf("first entry %v, want pax_global_header", names)
	}
	top := "acme-web-0123456/"
	if contents[top+"Dockerfile"] != "FROM scratch\n" || contents[top+"src/main.go"] != "package main\n" {
		t.Errorf("files under %s missing: %v", top, contents)
	}
	for _, n := range names[1:] {
		if len(n) < len(top) || n[:len(top)] != top {
			t.Errorf("entry %q outside the top folder", n)
		}
	}
}

func TestHeadETag(t *testing.T) {
	f := arranged()
	ctx := context.Background()
	c, etag, changed, err := f.Head(ctx, 7, 42, "main", "")
	if err != nil || !changed || c.SHA != sha || etag == "" {
		t.Fatalf("first head = %v %q %v %v", c, etag, changed, err)
	}
	_, same, changed, err := f.Head(ctx, 7, 42, "main", etag)
	if err != nil || changed || same != etag {
		t.Fatalf("unchanged head = %q %v %v", same, changed, err)
	}
	next := source.Commit{SHA: "fedcba9876543210fedcba9876543210fedcba98", Title: "second", Author: "bo"}
	f.Push(42, "main", next)
	c, etag2, changed, err := f.Head(ctx, 7, 42, "main", etag)
	if err != nil || !changed || c != next || etag2 == etag {
		t.Fatalf("after push = %v %q %v %v", c, etag2, changed, err)
	}
	if _, _, _, err := f.Head(ctx, 7, 42, "gone", ""); !errx.IsType(err, errx.TypeNotFound) {
		t.Errorf("missing branch err = %v", err)
	}
}

func TestAccessAndUsers(t *testing.T) {
	f := arranged()
	ctx := context.Background()
	info := source.InstallationInfo{ID: 7, Account: "acme", AccountType: "Organization"}
	if err := f.ConfirmInstaller(ctx, "code-1", info); err != nil {
		t.Fatalf("owner err = %v", err)
	}
	if err := f.ConfirmInstaller(ctx, "other", info); !errx.IsType(err, errx.TypeAuthorization) {
		t.Errorf("unknown code err = %v", err)
	}
	if err := f.ConfirmInstaller(ctx, "code-1", source.InstallationInfo{ID: 8}); !errx.IsType(err, errx.TypeAuthorization) {
		t.Errorf("other installation err = %v", err)
	}
	repos, err := f.Repositories(ctx, 7)
	if err != nil || len(repos) != 1 || repos[0].FullName != "acme/web" {
		t.Fatalf("repos = %v %v", repos, err)
	}
	f.RemoveRepo(42)
	if _, err := f.Repository(ctx, 7, 42); !errx.IsType(err, errx.TypeNotFound) {
		t.Errorf("removed repo err = %v", err)
	}
}

func TestStatusesAndFail(t *testing.T) {
	f := arranged()
	ctx := context.Background()
	if err := f.SetStatus(ctx, 7, 42, sha, source.CommitStatus{State: source.CommitPending}); err != nil {
		t.Fatal(err)
	}
	if got := f.Statuses(42, sha); len(got) != 1 || got[0].State != source.CommitPending {
		t.Fatalf("statuses = %v", got)
	}
	boom := errx.External("boom")
	f.Fail("Head", boom)
	if _, _, _, err := f.Head(ctx, 7, 42, "main", ""); err != boom {
		t.Errorf("injected err = %v", err)
	}
	f.Fail("Head", nil)
	if _, _, _, err := f.Head(ctx, 7, 42, "main", ""); err != nil {
		t.Errorf("after clearing: %v", err)
	}
}

func TestWebhooks(t *testing.T) {
	f := arranged()
	body := sourcefake.PushPayload(7, 42, "acme/web", "main", source.Commit{SHA: sha, Title: "ship it", Author: "ana"})
	if err := f.VerifyWebhook(sourcefake.Sign("s3cret", body), body); err != nil {
		t.Fatal(err)
	}
	if err := f.VerifyWebhook(sourcefake.Sign("wrong", body), body); !errx.IsType(err, errx.TypeAuthorization) {
		t.Errorf("bad signature err = %v", err)
	}
	ev, err := f.PushEvent(body)
	want := source.PushEvent{Installation: 7, RepositoryID: 42, Repository: "acme/web", Branch: "main",
		Head: source.Commit{SHA: sha, Title: "ship it", Author: "ana"}, Sender: "ana"}
	if err != nil || ev != want {
		t.Fatalf("push = %+v %v", ev, err)
	}
	ie, err := f.InstallationEvent("installation_repositories", sourcefake.InstallationPayload("removed", 7, 42, 43))
	if err != nil || ie.Installation != 7 || ie.Action != "removed" || len(ie.Removed) != 2 {
		t.Fatalf("installation event = %+v %v", ie, err)
	}
	if _, err := f.PushEvent([]byte("{")); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("malformed err = %v", err)
	}
}

func TestDisabled(t *testing.T) {
	d := sourcefake.Disabled()
	if d.Enabled() {
		t.Fatal("enabled")
	}
	_, err := d.Repositories(context.Background(), 1)
	var e *errx.Error
	if !errx.As(err, &e) || e.Code != source.CodeNotConfigured || e.Type != errx.TypeBusiness {
		t.Errorf("err = %v", err)
	}
	if err := d.VerifyWebhook("x", nil); !errx.IsType(err, errx.TypeAuthorization) {
		t.Errorf("verify err = %v", err)
	}
}

func TestConcurrent(t *testing.T) {
	f := arranged()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			f.Push(42, "main", source.Commit{SHA: sha})
		}()
		go func() {
			defer wg.Done()
			_, _, _, _ = f.Head(context.Background(), 7, 42, "main", "")
		}()
	}
	wg.Wait()
}
