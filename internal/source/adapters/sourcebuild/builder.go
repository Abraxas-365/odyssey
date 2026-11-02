// Package sourcebuild is the source.Builder adapter on the build domain:
// imported commits are uploaded as build sources through build's Commands
// port only (never buildsvc).
package sourcebuild

import (
	"context"
	"io"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/source"
)

type Builder struct{ commands build.Commands }

func New(commands build.Commands) *Builder { return &Builder{commands: commands} }

var _ source.Builder = (*Builder)(nil)

func (b *Builder) Upload(ctx context.Context, organization identity.OrganizationID, id project.ID, input build.Upload, archive io.Reader) (build.Source, error) {
	return b.commands.Upload(ctx, organization, id, input, archive)
}

func (b *Builder) Discard(ctx context.Context, organization identity.OrganizationID, id build.SourceID) error {
	return b.commands.Discard(ctx, organization, id)
}
