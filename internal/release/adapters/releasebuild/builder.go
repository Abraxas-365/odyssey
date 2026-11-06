// Package releasebuild is the release.Builder adapter on the build domain:
// deployments from source upload, build and follow their image through
// build's Commands and Queries ports only (never buildsvc), like
// releasefleet does with the fleet.
package releasebuild

import (
	"context"
	"io"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Builder struct {
	commands build.Commands
	queries  build.Queries
}

func New(commands build.Commands, queries build.Queries) *Builder {
	return &Builder{commands: commands, queries: queries}
}

var _ release.Builder = (*Builder)(nil)

func (b *Builder) Upload(ctx context.Context, organization identity.OrganizationID, id project.ID, input build.Upload, archive io.Reader) (build.Source, error) {
	return b.commands.Upload(ctx, organization, id, input, archive)
}

func (b *Builder) Start(ctx context.Context, organization identity.OrganizationID, input build.Start) error {
	_, err := b.commands.Start(ctx, organization, input)
	return err
}

func (b *Builder) Status(ctx context.Context, organization identity.OrganizationID, id build.ID) (build.Build, error) {
	return b.queries.Find(ctx, organization, id)
}

// Cancel of a build that never started (its deployment was replaced before
// the build step ran) has nothing to stop.
func (b *Builder) Cancel(ctx context.Context, organization identity.OrganizationID, id build.ID, input build.Cancel) error {
	err := b.commands.Cancel(ctx, organization, id, input)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	return err
}

func (b *Builder) Discard(ctx context.Context, organization identity.OrganizationID, source build.SourceID) error {
	return b.commands.Discard(ctx, organization, source)
}

func (b *Builder) Source(ctx context.Context, organization identity.OrganizationID, source build.SourceID) (build.Source, error) {
	return b.queries.FindSource(ctx, organization, source)
}

func (b *Builder) Derive(ctx context.Context, organization identity.OrganizationID, input build.Derive) (build.Source, error) {
	return b.commands.Derive(ctx, organization, input)
}
