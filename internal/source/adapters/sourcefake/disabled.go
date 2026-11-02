package sourcefake

import (
	"context"
	"io"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// Disabled is the GitProvider of an Odyssey without a GitHub App: Enabled is
// false and everything else refuses with GITHUB_NOT_CONFIGURED.
func Disabled() source.GitProvider { return disabled{} }

type disabled struct{}

func notConfigured() error {
	return errx.Business(source.Message(source.CodeNotConfigured)).WithCode(source.CodeNotConfigured)
}

func (disabled) Enabled() bool             { return false }
func (disabled) Slug() string              { return "" }
func (disabled) InstallPage(string) string { return "" }
func (disabled) ConfirmInstaller(context.Context, string, source.InstallationInfo) error {
	return notConfigured()
}
func (disabled) Installation(context.Context, int64) (source.InstallationInfo, error) {
	return source.InstallationInfo{}, notConfigured()
}
func (disabled) Repositories(context.Context, int64) ([]source.Repo, error) {
	return nil, notConfigured()
}
func (disabled) Repository(context.Context, int64, int64) (source.Repo, error) {
	return source.Repo{}, notConfigured()
}
func (disabled) Branches(context.Context, int64, int64) ([]source.Branch, error) {
	return nil, notConfigured()
}
func (disabled) Head(context.Context, int64, int64, string, string) (source.Commit, string, bool, error) {
	return source.Commit{}, "", false, notConfigured()
}
func (disabled) Tarball(context.Context, int64, int64, string) (io.ReadCloser, error) {
	return nil, notConfigured()
}
func (disabled) SetStatus(context.Context, int64, int64, string, source.CommitStatus) error {
	return notConfigured()
}
func (disabled) VerifyWebhook(string, []byte) error {
	return errx.Unauthorized("invalid webhook signature")
}
func (disabled) PushEvent([]byte) (source.PushEvent, error) {
	return source.PushEvent{}, notConfigured()
}
func (disabled) InstallationEvent(string, []byte) (source.InstallationEvent, error) {
	return source.InstallationEvent{}, notConfigured()
}
