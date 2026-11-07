package project

import (
	"strings"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/pkg/detect"
)

// Detection (MS-5): the platform holds an app's services (D10); the console
// and assistants propose them from the code's file names, and a person or
// the assistant confirms with SetServices. A proposal writes nothing.

// MaxDetectPath bounds one path given to Detect.
const MaxDetectPath = 4096

// MaxDetectPaths is how many paths Detect looks at (a provider listing a
// repository stops there and says the list was truncated).
const MaxDetectPaths = detect.MaxPaths

// DetectServices asks for a proposal from the paths of the code (a dropped
// folder's files, a repository's tree), under Root.
type DetectServices struct {
	Paths []string `json:"paths"`
	// Root is the app's folder inside the paths ("" for all of them).
	Root string `json:"root"`
}

func (d DetectServices) Normalize() DetectServices {
	d.Root = NormalizeRoot(d.Root)
	return d
}

func (d DetectServices) Validate() error {
	if len(d.Paths) > MaxDetectPaths {
		return errx.InvalidField("paths", "at most 20000 paths")
	}
	for _, p := range d.Paths {
		if len(p) > MaxDetectPath || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
			return errx.InvalidField("paths", "a path is up to 4096 bytes of text")
		}
	}
	return ValidRoot(d.Root)
}

// DetectedService is one proposed service and the file that made its folder
// an app.
type DetectedService struct {
	ServiceSpec
	Evidence string `json:"evidence"`
}

// Detected is a proposal: the services (ready for SetServices, in its
// order) and plain-Spanish notes on what it left out or guessed.
type Detected struct {
	Items []DetectedService `json:"items"`
	Notes []string          `json:"notes"`
	// Truncated: the paths were not all of the code (a provider cut the
	// listing short), so the proposal may miss services.
	Truncated bool `json:"truncated"`
}

// TruncatedNote is the note of a proposal made from part of the code.
const TruncatedNote = "El repositorio es muy grande y GitHub no listó todos sus archivos: puede faltar algún servicio."

// Detect proposes the services of the code whose files are d.Paths. It is
// pure: the same paths give the same proposal.
func Detect(d DetectServices) Detected {
	p := detect.Services(d.Paths, d.Root)
	out := Detected{Items: make([]DetectedService, 0, len(p.Services)), Notes: append([]string{}, p.Notes...)}
	for _, s := range p.Services {
		out.Items = append(out.Items, DetectedService{ServiceSpec: ServiceSpec{Name: s.Name, Kind: ServiceKind(s.Kind), Root: s.Root,
			Route: s.Route, StripPrefix: s.StripPrefix}, Evidence: s.Evidence})
	}
	return out
}
