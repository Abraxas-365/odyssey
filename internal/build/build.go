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
}

// Expired reports whether the source's bytes are gone (or going).
func (s Source) Expired() bool { return s.ExpiredAt != nil }

// Upload describes a source being uploaded; its bytes come as a stream.
type Upload struct {
	Name       string
	UploadedBy identity.UserID
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
	return u
}

func (u Upload) Validate() error {
	if !utf8.ValidString(u.Name) || u.Name == "" || len(u.Name) > MaxName {
		return errx.InvalidField("name", "name must be up to 200 bytes of text")
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
}

// Job is everything a runner needs to know of a build.
type Job struct {
	Build  Build
	Source Source
}

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
