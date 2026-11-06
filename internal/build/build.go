// Package build turns source code into an image, in the customer's own
// cluster: a Source is an uploaded archive (a folder as tar.gz); a Build is
// one attempt to make an image of it. The build.run workflow (buildsvc)
// hands the build to a Runner (the agent of the environment's cluster, or a
// fake), follows it, archives its logs and records the outcome. Deployments
// from code (release) start a build and wait for its image.
package build

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Limits of an uploaded source.
const (
	// MaxSourceBytes is the largest archive accepted (compressed).
	MaxSourceBytes = 100 << 20
	// MaxUnpackedBytes bounds what the archive expands to.
	MaxUnpackedBytes = 500 << 20
	// MaxSourceFiles bounds the entries of the archive.
	MaxSourceFiles = 20_000
	// MaxName is the longest display name of a source.
	MaxName = 200
)

// Source is an uploaded archive of an app's code. Its bytes live in blob
// storage under Key; the row is the index. Sources are swept after a while:
// the image a build made of one outlives it.
type Source struct {
	ID           SourceID                `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	// Name is the folder or file name shown in the console (display only).
	Name         string          `json:"name" db:"name"`
	Size         int64           `json:"size" db:"size"`
	SHA256       string          `json:"sha256" db:"sha256"`
	Files        int             `json:"files" db:"files"`
	UnpackedSize int64           `json:"unpacked_size" db:"unpacked_size"`
	Key          string          `json:"-" db:"blob_key"`
	UploadedBy   identity.UserID `json:"uploaded_by" db:"uploaded_by"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
	// ExpiredAt is when the sweep dropped its bytes (nil while available).
	ExpiredAt *time.Time `json:"expired_at" db:"expired_at"`
	// Git is the commit the source was taken from (nil for an uploaded folder).
	Git *GitRef `json:"git" db:"-"`
	// Parent is the source this one was cut from (nil for an upload): a
	// service with a folder builds only that folder (Root) of its app's code,
	// re-rooted. Derive makes it, once per parent and folder.
	Parent *SourceID `json:"parent_id" db:"parent_id"`
	Root   string    `json:"root" db:"root"`
	// ContentDigest identifies its files whatever packed them
	// (srcarchive.Summary.ContentDigest): builds of sources with the same one
	// may share an image.
	ContentDigest string `json:"content_digest" db:"content_digest"`
}

// Git providers a source can come from.
const ProviderGitHub = "github"

// Limits of a GitRef's text.
const (
	MaxRepository  = 200 // "owner/name"
	MaxBranch      = 255
	MaxCommitTitle = 200 // the first line of the message, cut
	MaxAuthor      = 100
)

var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// GitRef is a commit of a repository: where a source from git came from. It
// is display and provenance only; the bytes are what the source holds.
type GitRef struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"` // "owner/name"
	Branch     string `json:"branch"`
	Commit     string `json:"commit"` // full SHA (40 or 64 hex)
	// Title is the first line of the commit message (cut to MaxCommitTitle).
	Title  string `json:"title"`
	Author string `json:"author"` // the author's login, or name when it has none
}

// Normalize keeps Title to the first line of the message, cut, and trims.
func (g GitRef) Normalize() GitRef {
	g.Repository, g.Branch, g.Author = strings.TrimSpace(g.Repository), strings.TrimSpace(g.Branch), strings.TrimSpace(g.Author)
	g.Commit = strings.ToLower(strings.TrimSpace(g.Commit))
	title, _, _ := strings.Cut(g.Title, "\n")
	g.Title = cut(strings.TrimSpace(strings.Map(dropControl, title)), MaxCommitTitle)
	g.Author = cut(strings.Map(dropControl, g.Author), MaxAuthor)
	return g
}

func (g GitRef) Validate() error {
	switch {
	case g.Provider != ProviderGitHub:
		return errx.InvalidField("git.provider", "provider must be github")
	case g.Repository == "" || len(g.Repository) > MaxRepository || strings.Count(g.Repository, "/") != 1 || !utf8.ValidString(g.Repository):
		return errx.InvalidField("git.repository", "repository must be owner/name")
	case g.Branch == "" || len(g.Branch) > MaxBranch || !utf8.ValidString(g.Branch):
		return errx.InvalidField("git.branch", "branch must be up to 255 bytes of text")
	case !commitPattern.MatchString(g.Commit):
		return errx.InvalidField("git.commit", "commit must be a full SHA")
	case len(g.Title) > MaxCommitTitle*4 || !utf8.ValidString(g.Title) || len(g.Author) > MaxAuthor*4 || !utf8.ValidString(g.Author):
		return errx.InvalidField("git", "commit title and author must be text")
	}
	return nil
}

// ShortCommit is the commit's first 7 characters.
func (g GitRef) ShortCommit() string {
	if len(g.Commit) < 7 {
		return g.Commit
	}
	return g.Commit[:7]
}

func dropControl(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}

// cut keeps at most n runes of s.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Expired reports whether the source's bytes are gone (or going).
func (s Source) Expired() bool { return s.ExpiredAt != nil }

// Upload describes a source being uploaded; its bytes come as a stream.
type Upload struct {
	Name       string
	UploadedBy identity.UserID
	// Git is the commit the archive was taken from (nil for a folder).
	Git *GitRef
}

// Normalize cleans the display name: base name only, no control characters,
// at most MaxName bytes of valid UTF-8 ("código" when nothing is left).
func (u Upload) Normalize() Upload {
	name := u.Name
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' {
			return -1
		}
		return r
	}, name))
	for _, ext := range []string{".tar.gz", ".tgz"} {
		if trimmed := strings.TrimSuffix(name, ext); trimmed != "" {
			name = trimmed
		}
	}
	if len(name) > MaxName {
		name = strings.ToValidUTF8(name[:MaxName], "")
	}
	if name == "" || name == "." || name == ".." {
		name = "código"
	}
	u.Name = name
	if u.Git != nil {
		g := u.Git.Normalize()
		u.Git = &g
	}
	return u
}

func (u Upload) Validate() error {
	if !utf8.ValidString(u.Name) || u.Name == "" || len(u.Name) > MaxName {
		return errx.InvalidField("name", "name must be up to 200 bytes of text")
	}
	if u.Git != nil {
		return u.Git.Validate()
	}
	return nil
}

// Status is a build's lifecycle:
//
//	queued → running → succeeded | failed
//	(any unfinished) → cancelled: a newer deployment no longer needs it
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Finished() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Failure is why a build did not produce an image: Message in plain Spanish
// for whoever publishes, Detail the builder's own words.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

// Build is one attempt to make an image of a source, in the cluster of the
// environment it is for.
type Build struct {
	ID           ID                      `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	Source       SourceID                `json:"source_id" db:"source_id"`
	Status       Status                  `json:"status" db:"status"`
	// Image is the result, pinned by digest ("" until it succeeds).
	Image       string          `json:"image" db:"image"`
	Error       *Failure        `json:"error" db:"-"`
	Run         workflow.RunID  `json:"run_id" db:"run_id"`
	RequestedBy identity.UserID `json:"requested_by" db:"requested_by"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at" db:"updated_at"`
	StartedAt   *time.Time      `json:"started_at" db:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at" db:"finished_at"`
	// LogsKey is where the finished build's logs are archived ("" while live).
	LogsKey string `json:"-" db:"logs_key"`
	// LogsIndex says where to start reading the archive for a seq.
	LogsIndex LogIndex `json:"-" db:"-"`
	// Scan is the vulnerability scan of the image; nil until it is recorded
	// (and always for a build that did not succeed).
	Scan *Scan `json:"scan" db:"-"`
	// ContentDigest is its source's (what reuse matches on; "" for sources
	// from before it was computed: never reused).
	ContentDigest string `json:"-" db:"content_digest"`
	// Rebuild: never reuse an earlier image, build again.
	Rebuild bool `json:"rebuild" db:"rebuild"`
	// BuilderIdentity names the builder that made the image (succeeded
	// only; "" when it announced none: its image is never reused).
	BuilderIdentity string `json:"-" db:"builder_identity"`
	// ReusedFrom is the earlier build whose image this one reused instead
	// of building (succeeded only).
	ReusedFrom *ID `json:"reused_from" db:"reused_from"`
}

// Candidate is an earlier build whose image a build of the same code may
// reuse: the latest successful one per builder identity.
type Candidate struct {
	Build    ID     `db:"id"`
	Image    string `db:"image"`
	Identity string `db:"builder_identity"`
	// Scan is its scan (nil when none was recorded yet).
	Scan *Scan `db:"-"`
}

// ScanReuseWithin: a reused image's scan is copied when it is known and at
// most this old; otherwise the reused image is scanned again.
const ScanReuseWithin = 7 * 24 * time.Hour

// CopiesScan reports whether a build reusing c at now copies its scan.
func (c Candidate) CopiesScan(now time.Time) bool {
	return c.Scan != nil && c.Scan.Known() && now.Sub(c.Scan.ScannedAt) <= ScanReuseWithin
}

// Reuse is what the runner is told it may reuse: the candidates, one per
// builder identity (it uses the one its cluster's builder made, if any).
type Reuse struct {
	Image    string
	Identity string
	// SkipScan: the build domain copies the candidate's scan.
	SkipScan bool
}

// LogMark is a line of an archived log: its seq and the byte it starts at.
type LogMark struct {
	Seq    int64 `json:"seq"`
	Offset int64 `json:"offset"`
}

// LogIndex marks every LogIndexEvery-th line of an archived log, so a page
// is read from near its first line rather than from the top. Marks are in
// order; an empty index reads from the top.
type LogIndex []LogMark

// LogIndexEvery is how many lines apart the marks are.
const LogIndexEvery = 1000

// Start is where to read from for the lines after seq: the last mark no
// later than the first line wanted (seqs only grow, so every line before it
// is at or below after). The zero mark is the top.
func (x LogIndex) Start(after int64) LogMark {
	var start LogMark
	for _, m := range x {
		if m.Seq > after+1 {
			break
		}
		start = m
	}
	return start
}

// Usage is how much source an organization keeps: Stored counts every
// unexpired source; Evictable, those a full quota may expire early (built,
// and no unfinished build needs them).
type Usage struct {
	Stored    int64 `db:"stored"`
	Evictable int64 `db:"evictable"`
}

// Start asks to build a source for an environment. ID is chosen by the
// caller, so asking again with the same ID returns the same build.
type Start struct {
	ID          ID
	Project     project.ID
	Environment project.EnvironmentID
	Source      SourceID
	RequestedBy identity.UserID
	// Rebuild builds even when an earlier build of the same code could be
	// reused (a newer base image, a fix outside the code).
	Rebuild bool
}

func (s Start) Validate() error {
	switch {
	case s.ID.IsZero():
		return errx.InvalidField("id", "build id is required")
	case s.Project.IsZero(), s.Environment.IsZero():
		return errx.Validation("build needs a project and an environment")
	case s.Source.IsZero():
		return errx.InvalidField("source_id", "source_id is required")
	}
	return nil
}

// Cancel asks to stop a build nobody needs any more.
type Cancel struct {
	CancelledBy identity.UserID
}

func (Cancel) Validate() error { return nil }

// Derive asks for the source of one folder (Root) of an uploaded source
// (Parent): what a service whose code lives in that folder builds.
type Derive struct {
	Parent      SourceID
	Root        string
	RequestedBy identity.UserID
}

func (d Derive) Validate() error {
	switch {
	case d.Parent.IsZero():
		return errx.InvalidField("source_id", "source_id is required")
	case d.Root == "" || project.ValidRoot(d.Root) != nil:
		return errx.InvalidField("root", "root must be a folder of the code")
	}
	return nil
}

// WorkflowRun is the workflow that drives a build (buildsvc).
const WorkflowRun = "build.run"

// WorkflowArchive moves a finished build's log from the runner to storage.
// It is its own run so every way a build ends (success, failure, cancel,
// timeout) gets there the same way.
const WorkflowArchive = "build.archive"

// RunInput is the build.run workflow's input.
type RunInput struct {
	Build ID `json:"build_id"`
}

// Steps of build.run, in order.
const (
	StepDispatch = "dispatch" // hand the build to the runner
	StepWait     = "wait"     // follow it until it ends
	StepFinish   = "finish"   // record the outcome
	StepArchive  = "archive"  // move the log to blob storage, let the runner forget it
)

// pinned is "<repository>@sha256:<64 hex>".
var pinned = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*[a-z0-9]@sha256:[a-f0-9]{64}$`)

// PinnedImage reports whether image is pinned by digest (what a build must produce).
func PinnedImage(image string) bool { return len(image) <= 512 && pinned.MatchString(image) }

// Timeout is the longest a build may run before it is failed (the runner's
// own deadline is a little shorter, so its words arrive first).
const Timeout = 25 * time.Minute

// Phase is what the runner reports of a build.
type Phase string

const (
	PhasePending   Phase = "pending"   // accepted, not running yet (or unknown to the runner)
	PhaseRunning   Phase = "running"   // preparing or building
	PhaseSucceeded Phase = "succeeded" // the image is pushed
	PhaseFailed    Phase = "failed"
)

// Reasons a runner gives for a failed build.
const (
	ReasonUnsupported = "unsupported" // no start command / unknown kind of app
	ReasonSource      = "source"      // the source could not be fetched or is invalid
	ReasonPush        = "push"        // the registry refused the image
	ReasonTimeout     = "timeout"     // the runner's deadline passed
	ReasonFailed      = "failed"      // a build step failed
)

// Observation is what the runner says of a build.
type Observation struct {
	Phase Phase
	// Image is the pushed image pinned by digest (succeeded only).
	Image string
	// Reason (failed only) is one of the Reason* constants.
	Reason string
	// Message is the runner's own words ("" when it said nothing).
	Message string
	// Reused is the image the runner reused instead of building (one of
	// Job.Reuse's; succeeded only).
	Reused string `json:",omitempty"`
	// Builder is the identity of the builder that made the image ("" when
	// unknown: then it is never reused).
	Builder string `json:",omitempty"`
}

// Job is everything a runner needs to know of a build.
type Job struct {
	Build  Build
	Source Source
	// Reuse are images of earlier builds of the same code the runner may
	// reuse instead of building (at most MaxReuse; none: it builds).
	Reuse []Reuse
}

// MaxReuse bounds the candidates a build considers.
const MaxReuse = 8

// LogLine is one line a build printed. Seq orders the lines of a build and
// is stable: a runner that resends lines resends the same numbers.
type LogLine struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"` // prepare | build | system
	Text   string `json:"text"`
}

// MaxLogPage is the most lines one Logs call returns.
const MaxLogPage = 1000

// LogPage is a slice of a build's log after a sequence number.
type LogPage struct {
	Lines []LogLine `json:"lines"`
	// Next is the seq to ask after next time (the last line's, or the one asked).
	Next int64 `json:"next"`
	// Finished: the build ended and Lines reach the end of its log.
	Finished bool `json:"finished"`
}

// LogQuery asks for a page of a build's log.
type LogQuery struct {
	After int64
	Limit int
}

func (q LogQuery) Normalize() LogQuery {
	if q.Limit <= 0 || q.Limit > MaxLogPage {
		q.Limit = MaxLogPage
	}
	return q
}

func (q LogQuery) Validate() error {
	if q.After < 0 {
		return errx.InvalidField("after", "after must be 0 or more")
	}
	return nil
}
