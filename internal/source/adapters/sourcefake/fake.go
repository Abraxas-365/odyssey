// Package sourcefake is an in-memory source.GitProvider for tests and for
// development without GitHub. It keeps installations, repositories,
// branches and files in memory, builds GitHub-shaped tarballs on the fly and
// understands the same webhook JSON GitHub sends (so tests can feed real
// shaped payloads). Disabled stands in when no GitHub App is configured.
package sourcefake

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// Options configure the fake.
type Options struct {
	// Slug is the App's name in URLs (default "odyssey-fake").
	Slug string
	// WebhookSecret signs and verifies deliveries (default "fake-secret").
	WebhookSecret string
}

type repoState struct {
	installation int64
	repo         source.Repo
	branches     map[string]source.Commit
	etags        map[string]int // per branch, bumped on every Push
}

type statusKey struct {
	repo   int64
	commit string
}

// Fake is the in-memory provider; safe for concurrent use.
type Fake struct {
	opts Options

	mu            sync.Mutex
	installations map[int64]source.InstallationInfo
	owners        map[string]map[int64]bool // OAuth code → installations its user owns or administers
	repos         map[int64]*repoState
	files         map[statusKey]map[string]string
	truncated     map[int64]bool // repositories whose Tree is cut short
	statuses      map[statusKey][]source.CommitStatus
	failures      map[string]error
}

var _ source.GitProvider = (*Fake)(nil)

func New(opts Options) *Fake {
	if opts.Slug == "" {
		opts.Slug = "odyssey-fake"
	}
	if opts.WebhookSecret == "" {
		opts.WebhookSecret = "fake-secret"
	}
	return &Fake{
		opts:          opts,
		installations: map[int64]source.InstallationInfo{},
		owners:        map[string]map[int64]bool{},
		repos:         map[int64]*repoState{},
		files:         map[statusKey]map[string]string{},
		truncated:     map[int64]bool{},
		statuses:      map[statusKey][]source.CommitStatus{},
		failures:      map[string]error{},
	}
}

// ------------------------------------------------------------------ arrange

// AddInstallation registers an installation; owners are the OAuth codes
// whose GitHub user owns or administers its account (what ConfirmInstaller
// requires).
func (f *Fake) AddInstallation(info source.InstallationInfo, owners ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installations[info.ID] = info
	for _, code := range owners {
		if f.owners[code] == nil {
			f.owners[code] = map[int64]bool{}
		}
		f.owners[code][info.ID] = true
	}
}

// AddRepo makes the installation reach repo, with its branches.
func (f *Fake) AddRepo(installation int64, repo source.Repo, branches map[string]source.Commit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &repoState{installation: installation, repo: repo, branches: map[string]source.Commit{}, etags: map[string]int{}}
	for name, c := range branches {
		r.branches[name] = c
		r.etags[name] = 1
	}
	f.repos[repo.ID] = r
}

// RemoveRepo takes the repository away from its installation.
func (f *Fake) RemoveRepo(repoID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.repos, repoID)
}

// SetFiles sets the files of a commit (path → content) that Tarball serves.
func (f *Fake) SetFiles(repoID int64, commit string, files map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[statusKey{repoID, commit}] = files
}

// Push moves the branch to commit c (creating it) and bumps its ETag.
func (f *Fake) Push(repoID int64, branch string, c source.Commit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[repoID]
	if !ok {
		return
	}
	r.branches[branch] = c
	r.etags[branch]++
}

// Statuses are the commit statuses set on a commit, oldest first.
func (f *Fake) Statuses(repoID int64, commit string) []source.CommitStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]source.CommitStatus(nil), f.statuses[statusKey{repoID, commit}]...)
}

// Fail makes the next calls of a method (its name on source.GitProvider,
// "Head", "Tarball", ...) return err; nil stops failing.
func (f *Fake) Fail(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failures, method)
		return
	}
	f.failures[method] = err
}

// ------------------------------------------------------------- GitProvider

func (f *Fake) Enabled() bool { return true }
func (f *Fake) Slug() string  { return f.opts.Slug }

func (f *Fake) InstallPage(state string) string {
	return "https://github.fake/apps/" + f.opts.Slug + "/installations/new?state=" + url.QueryEscape(state)
}

// ConfirmInstaller accepts a code registered as an owner of the installation
// (see AddInstallation); any other code (a mere member, someone else, an
// unknown one) is NotYours.
func (f *Fake) ConfirmInstaller(_ context.Context, code string, installation source.InstallationInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ConfirmInstaller"]; err != nil {
		return err
	}
	if !f.owners[code][installation.ID] {
		return notYours()
	}
	return nil
}

func (f *Fake) Installation(_ context.Context, installation int64) (source.InstallationInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Installation"]; err != nil {
		return source.InstallationInfo{}, err
	}
	info, ok := f.installations[installation]
	if !ok {
		return source.InstallationInfo{}, noAccess()
	}
	return info, nil
}

func (f *Fake) Repositories(_ context.Context, installation int64) ([]source.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Repositories"]; err != nil {
		return nil, err
	}
	if _, ok := f.installations[installation]; !ok {
		return nil, noAccess()
	}
	var out []source.Repo
	for _, r := range f.repos {
		if r.installation == installation {
			out = append(out, r.repo)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Fake) Repository(_ context.Context, installation, repository int64) (source.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Repository"]; err != nil {
		return source.Repo{}, err
	}
	r, err := f.reach(installation, repository)
	if err != nil {
		return source.Repo{}, err
	}
	return r.repo, nil
}

func (f *Fake) Branches(_ context.Context, installation, repository int64) ([]source.Branch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Branches"]; err != nil {
		return nil, err
	}
	r, err := f.reach(installation, repository)
	if err != nil {
		return nil, err
	}
	out := make([]source.Branch, 0, len(r.branches))
	for name, c := range r.branches {
		out = append(out, source.Branch{Name: name, Commit: c.SHA})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Tree lists the files of ref (a branch, resolved to its commit, or a
// commit) as SetFiles stored them, sorted; a TreeTruncated repository says
// it was cut short.
func (f *Fake) Tree(_ context.Context, installation, repository int64, ref string) ([]string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Tree"]; err != nil {
		return nil, false, err
	}
	r, err := f.reach(installation, repository)
	if err != nil {
		return nil, false, err
	}
	commit := ref
	if c, ok := r.branches[ref]; ok {
		commit = c.SHA
	}
	files, ok := f.files[statusKey{repository, commit}]
	if !ok {
		return nil, false, errx.NotFound(source.Message(source.CodeBranchMissing)).WithCode(source.CodeBranchMissing)
	}
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, f.truncated[repository], nil
}

// TreeTruncated makes Tree of a repository say the listing was cut short.
func (f *Fake) TreeTruncated(repoID int64, truncated bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.truncated[repoID] = truncated
}

func (f *Fake) Head(_ context.Context, installation, repository int64, branch, etag string) (source.Commit, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Head"]; err != nil {
		return source.Commit{}, etag, false, err
	}
	r, err := f.reach(installation, repository)
	if err != nil {
		return source.Commit{}, etag, false, err
	}
	c, ok := r.branches[branch]
	if !ok {
		return source.Commit{}, etag, false, errx.NotFound(source.Message(source.CodeBranchMissing)).WithCode(source.CodeBranchMissing)
	}
	current := `W/"` + strconv.Itoa(r.etags[branch]) + `"`
	if etag == current {
		return source.Commit{}, etag, false, nil
	}
	return c, current, true, nil
}

// Tarball builds the commit's tarball as GitHub does (see tarball.go).
func (f *Fake) Tarball(_ context.Context, installation, repository int64, commit string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["Tarball"]; err != nil {
		return nil, err
	}
	r, err := f.reach(installation, repository)
	if err != nil {
		return nil, err
	}
	data, err := buildTarball(r.repo.FullName, commit, f.files[statusKey{repository, commit}])
	if err != nil {
		return nil, errx.Wrap(err, source.Message(source.CodeImportFailed), errx.TypeExternal).WithCode(source.CodeImportFailed)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *Fake) SetStatus(_ context.Context, installation, repository int64, commit string, status source.CommitStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["SetStatus"]; err != nil {
		return err
	}
	if _, err := f.reach(installation, repository); err != nil {
		return err
	}
	if r := []rune(status.Description); len(r) > 140 {
		status.Description = string(r[:140])
	}
	k := statusKey{repository, commit}
	f.statuses[k] = append(f.statuses[k], status)
	return nil
}

// reach is the repository when the installation reaches it (caller holds mu).
func (f *Fake) reach(installation, repository int64) (*repoState, error) {
	r, ok := f.repos[repository]
	if !ok || r.installation != installation {
		return nil, noAccess()
	}
	if info, ok := f.installations[installation]; !ok || info.Suspended {
		return nil, noAccess()
	}
	return r, nil
}

func noAccess() error {
	return errx.NotFound(source.Message(source.CodeNoAccess)).WithCode(source.CodeNoAccess)
}

func notYours() error {
	return errx.Unauthorized(source.Message(source.CodeNotYours)).WithCode(source.CodeNotYours)
}
