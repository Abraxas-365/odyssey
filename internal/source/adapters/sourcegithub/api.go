package sourcegithub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// ConfirmInstaller proves, with the user's own OAuth token, that the GitHub
// user owns the installation's account: the installation is among the ones
// the user reaches (/user/installations) and the user is the account itself
// (User) or an active admin of it (Organization; needs the App's
// members:read). Reaching it as a plain member is not enough.
func (p *Provider) ConfirmInstaller(ctx context.Context, code string, installation source.InstallationInfo) error {
	token, err := p.userToken(ctx, code)
	if err != nil {
		return err
	}
	listed := false
	err = pages(func(page int) (int, error) {
		var out struct {
			Installations []*github.Installation `json:"installations"`
		}
		resp, err := p.userCall(ctx, token, paged("user/installations", page), &out)
		if err != nil {
			return 0, err
		}
		for _, in := range out.Installations {
			listed = listed || in.GetID() == installation.ID
		}
		if listed {
			return 0, nil
		}
		return resp.NextPage, nil
	})
	if err != nil {
		return err
	}
	if !listed {
		return notYours(nil)
	}
	switch installation.AccountType {
	case "User":
		var me struct {
			Login string `json:"login"`
		}
		if _, err := p.userCall(ctx, token, "user", &me); err != nil {
			return err
		}
		if me.Login == "" || !strings.EqualFold(me.Login, installation.Account) {
			return notYours(nil)
		}
	case "Organization":
		var m struct {
			State string `json:"state"`
			Role  string `json:"role"`
		}
		if _, err := p.userCall(ctx, token, "user/memberships/orgs/"+url.PathEscape(installation.Account), &m); err != nil {
			return err
		}
		if m.State != "active" || m.Role != "admin" {
			return notYours(nil)
		}
	default:
		return notYours(nil)
	}
	return nil
}

// userCall GETs path with the user's token. A refusal of the token or of the
// resource (401, 403, 404) means the user is not the owner: NotYours;
// anything else (rate limits, 5xx, transport) is the provider failing.
func (p *Provider) userCall(ctx context.Context, token, path string, out any) (*github.Response, error) {
	resp, err := p.call(ctx, token, http.MethodGet, path, nil, out)
	if err == nil {
		return resp, nil
	}
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		switch er.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			if er.Response.Header.Get(github.HeaderRateRemaining) != "0" &&
				!strings.Contains(strings.ToLower(er.Message), "rate limit") {
				return nil, notYours(err)
			}
		}
	}
	return nil, p.fail(ctx, err, source.CodeNotYours)
}

func notYours(cause error) error {
	e := errx.Unauthorized(source.Message(source.CodeNotYours)).WithCode(source.CodeNotYours)
	e.Err = redact(cause)
	return e
}

func (p *Provider) userToken(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", notYours(nil)
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     p.cfg.ClientID,
		"client_secret": p.clientSecret,
		"code":          code,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.webURL+"/login/oauth/access_token", strings.NewReader(string(body)))
	if err != nil {
		return "", errx.Wrap(err, "build the GitHub OAuth request", errx.TypeInternal)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "odyssey")
	resp, err := p.plain.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", down(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", down(err)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return "", down(fmt.Errorf("GitHub OAuth answered %d", resp.StatusCode))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", down(fmt.Errorf("GitHub OAuth answered %d with a body that is not JSON", resp.StatusCode))
	}
	if out.AccessToken == "" {
		// bad_verification_code, incorrect_client_credentials, ...: the code
		// is not valid (the detail is the error name, never a secret).
		return "", notYours(fmt.Errorf("GitHub OAuth refused the code: %s", out.Error))
	}
	return out.AccessToken, nil
}

// Installation describes an installation as the App itself (its JWT).
func (p *Provider) Installation(ctx context.Context, id int64) (source.InstallationInfo, error) {
	app, err := p.appJWT()
	if err != nil {
		return source.InstallationInfo{}, err
	}
	var in github.Installation
	if _, err := p.call(ctx, app, http.MethodGet, fmt.Sprintf("app/installations/%d", id), nil, &in); err != nil {
		return source.InstallationInfo{}, p.fail(ctx, err, source.CodeNoAccess)
	}
	accountType := in.GetAccount().GetType()
	if accountType == "" {
		accountType = in.GetTargetType()
	}
	return source.InstallationInfo{
		ID:              id,
		Account:         in.GetAccount().GetLogin(),
		AccountType:     accountType,
		AllRepositories: in.GetRepositorySelection() == "all",
		Suspended:       in.SuspendedAt != nil,
	}, nil
}

// Repositories lists what the installation reaches (at most 1000).
func (p *Provider) Repositories(ctx context.Context, installation int64) ([]source.Repo, error) {
	token, err := p.installationToken(ctx, installation, 0, permsAll)
	if err != nil {
		return nil, err
	}
	var repos []source.Repo
	err = pages(func(page int) (int, error) {
		var out github.ListRepositories
		resp, err := p.call(ctx, token, http.MethodGet, paged("installation/repositories", page), nil, &out)
		if err != nil {
			return 0, p.fail(ctx, err, source.CodeNoAccess)
		}
		for _, r := range out.Repositories {
			repos = append(repos, repoOf(r))
		}
		if len(repos) >= maxItems {
			repos = repos[:maxItems]
			return 0, nil
		}
		return resp.NextPage, nil
	})
	return repos, err
}

func repoOf(r *github.Repository) source.Repo {
	return source.Repo{
		ID:            r.GetID(),
		FullName:      r.GetFullName(),
		DefaultBranch: r.GetDefaultBranch(),
		Private:       r.GetPrivate(),
	}
}

func (p *Provider) Repository(ctx context.Context, installation, repository int64) (source.Repo, error) {
	token, err := p.installationToken(ctx, installation, repository, permsRead)
	if err != nil {
		return source.Repo{}, err
	}
	return p.repository(ctx, token, repository)
}

func (p *Provider) repository(ctx context.Context, token string, repository int64) (source.Repo, error) {
	var r github.Repository
	if _, err := p.call(ctx, token, http.MethodGet, fmt.Sprintf("repositories/%d", repository), nil, &r); err != nil {
		return source.Repo{}, p.fail(ctx, err, source.CodeNoAccess)
	}
	return repoOf(&r), nil
}

// Branches lists the repository's branches (at most 1000).
func (p *Provider) Branches(ctx context.Context, installation, repository int64) ([]source.Branch, error) {
	token, err := p.installationToken(ctx, installation, repository, permsRead)
	if err != nil {
		return nil, err
	}
	repo, err := p.repository(ctx, token, repository)
	if err != nil {
		return nil, err
	}
	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || owner == "" || name == "" {
		return nil, down(fmt.Errorf("GitHub answered repository %d without a full name", repository))
	}
	path := fmt.Sprintf("repos/%s/%s/branches", url.PathEscape(owner), url.PathEscape(name))
	var branches []source.Branch
	err = pages(func(page int) (int, error) {
		var out []*github.Branch
		resp, err := p.call(ctx, token, http.MethodGet, paged(path, page), nil, &out)
		if err != nil {
			return 0, p.fail(ctx, err, source.CodeNoAccess)
		}
		for _, b := range out {
			branches = append(branches, source.Branch{Name: b.GetName(), Commit: b.GetCommit().GetSHA()})
		}
		if len(branches) >= maxItems {
			branches = branches[:maxItems]
			return 0, nil
		}
		return resp.NextPage, nil
	})
	return branches, err
}

// treeEntry is the part of a git tree entry Tree reads.
type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob, tree, commit (a submodule)
}

// Tree lists the files of the repository at ref with the git trees API
// (recursive). GitHub cuts a huge tree short (truncated); so does the cap.
func (p *Provider) Tree(ctx context.Context, installation, repository int64, ref string) ([]string, bool, error) {
	token, err := p.installationToken(ctx, installation, repository, permsRead)
	if err != nil {
		return nil, false, err
	}
	var out struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	path := fmt.Sprintf("repositories/%d/git/trees/%s?recursive=1", repository, url.PathEscape(ref))
	if _, err := p.call(ctx, token, http.MethodGet, path, nil, &out); err != nil {
		var re *github.ErrorResponse
		if errors.As(err, &re) && re.Response != nil &&
			(re.Response.StatusCode == http.StatusNotFound || re.Response.StatusCode == http.StatusUnprocessableEntity || re.Response.StatusCode == http.StatusConflict) {
			// 409: an empty repository has no tree
			return nil, false, errx.Wrap(err, source.Message(source.CodeBranchMissing), errx.TypeNotFound).WithCode(source.CodeBranchMissing)
		}
		return nil, false, p.fail(ctx, err, source.CodeNoAccess)
	}
	paths := make([]string, 0, min(len(out.Tree), project.MaxDetectPaths))
	truncated := out.Truncated
	for _, e := range out.Tree {
		if e.Type != "blob" {
			continue
		}
		if len(paths) == project.MaxDetectPaths {
			truncated = true
			break
		}
		paths = append(paths, e.Path)
	}
	return paths, truncated, nil
}

// Head is the newest commit of a branch; with etag, an unchanged branch
// answers 304 (which GitHub does not count against the rate limit).
func (p *Provider) Head(ctx context.Context, installation, repository int64, branch, etag string) (source.Commit, string, bool, error) {
	token, err := p.installationToken(ctx, installation, repository, permsRead)
	if err != nil {
		return source.Commit{}, etag, false, err
	}
	branchMissing := func(cause error) error {
		return errx.Wrap(cause, source.Message(source.CodeBranchMissing), errx.TypeNotFound).WithCode(source.CodeBranchMissing)
	}
	req, err := p.gh.NewRequest(ctx, http.MethodGet, fmt.Sprintf("repositories/%d/commits/%s", repository, url.PathEscape(branch)), nil)
	if err != nil {
		return source.Commit{}, etag, false, branchMissing(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := p.plain.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return source.Commit{}, etag, false, ctx.Err()
		}
		return source.Commit{}, etag, false, down(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return source.Commit{}, etag, false, nil
	}
	if err := github.CheckResponse(resp); err != nil {
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusUnprocessableEntity:
			return source.Commit{}, etag, false, branchMissing(err)
		}
		return source.Commit{}, etag, false, p.fail(ctx, err, source.CodeBranchMissing)
	}
	var rc github.RepositoryCommit
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rc); err != nil {
		return source.Commit{}, etag, false, down(fmt.Errorf("decode the commit of %q: %w", branch, err))
	}
	author := rc.GetAuthor().GetLogin()
	if author == "" {
		author = rc.GetCommit().GetAuthor().GetName()
	}
	return source.Commit{
		SHA:    rc.GetSHA(),
		Title:  firstLine(rc.GetCommit().GetMessage()),
		Author: author,
	}, resp.Header.Get("ETag"), true, nil
}

// Tarball streams the commit's tarball. GitHub answers 302 to codeload; the
// installation token is not sent there (see redirectWithoutCredentials).
func (p *Provider) Tarball(ctx context.Context, installation, repository int64, commit string) (io.ReadCloser, error) {
	token, err := p.installationToken(ctx, installation, repository, permsRead)
	if err != nil {
		return nil, err
	}
	importFailed := func(cause error) error {
		return errx.Wrap(redact(cause), source.Message(source.CodeImportFailed), errx.TypeExternal).WithCode(source.CodeImportFailed)
	}
	req, err := p.gh.NewRequest(ctx, http.MethodGet, fmt.Sprintf("repositories/%d/tarball/%s", repository, url.PathEscape(commit)), nil)
	if err != nil {
		return nil, importFailed(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, down(err)
	}
	if resp.StatusCode/100 == 2 {
		return resp.Body, nil
	}
	defer resp.Body.Close()
	status := fmt.Errorf("GitHub answered %d for the tarball", resp.StatusCode)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errx.Wrap(status, source.Message(source.CodeNoAccess), errx.TypeNotFound).WithCode(source.CodeNoAccess)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get(github.HeaderRateRemaining) == "0":
		return nil, down(status)
	}
	return nil, importFailed(status)
}

// SetStatus puts the "Odicea" commit status on the commit.
func (p *Provider) SetStatus(ctx context.Context, installation, repository int64, commit string, status source.CommitStatus) error {
	token, err := p.installationToken(ctx, installation, repository, permsStatus)
	if err != nil {
		return err
	}
	body := github.RepoStatus{
		State:       github.Ptr(status.State),
		Description: github.Ptr(cut(status.Description, maxStatusLen)),
		Context:     github.Ptr(source.StatusContext),
	}
	if status.TargetURL != "" {
		body.TargetURL = github.Ptr(status.TargetURL)
	}
	path := fmt.Sprintf("repositories/%d/statuses/%s", repository, url.PathEscape(commit))
	if _, err := p.call(ctx, token, http.MethodPost, path, &body, nil); err != nil {
		return p.fail(ctx, err, source.CodeNoAccess, http.StatusUnprocessableEntity)
	}
	return nil
}

// cut shortens s to at most n runes.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
