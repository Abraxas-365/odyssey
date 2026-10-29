package fleet

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Builder is a cluster's image builder, as its agent announced it: builds
// are pushed to Registry, which the cluster's nodes pull from.
type Builder struct {
	// Registry is host[:port][/path] (e.g. "registry.example.com/odyssey").
	Registry  string   `json:"registry"`
	Platforms []string `json:"platforms"`
}

// MaxRegistry bounds the announced registry.
const MaxRegistry = 200

var registryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

func (b Builder) Validate() error {
	if len(b.Registry) > MaxRegistry || !registryPattern.MatchString(b.Registry) {
		return errx.Validation("the builder's registry must be host[:port][/path] in lower case")
	}
	if len(b.Platforms) > 8 {
		return errx.Validation("too many builder platforms reported")
	}
	for _, p := range b.Platforms {
		if utf8.RuneCountInString(p) > 40 || !platformPattern.MatchString(p) {
			return errx.Validation("a builder platform is not valid")
		}
	}
	return nil
}

var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+(?:/[a-z0-9]+)?$`)

// Repository is where a project's images go in this registry.
func (b Builder) Repository(organization identity.OrganizationID, project project.ID) string {
	return b.Registry + "/" + organization.String() + "/" + project.String()
}

// Build limits.
const (
	// MaxBuildTimeout bounds what DispatchBuild may ask a cluster to wait for
	// a build.
	MaxBuildTimeout = time.Hour
	// MaxBuildLogBytes is what one build's log may keep; beyond it lines are
	// dropped and reading the log ends with a "log cut" line.
	MaxBuildLogBytes = 5 << 20
	// MaxLogLineBytes cuts longer lines.
	MaxLogLineBytes = 4 << 10
	// LogLineCost is the least a line counts against MaxBuildLogBytes, so
	// the cap also bounds the stored rows (about 52k, near what the build
	// domain archives).
	LogLineCost = 100
	// MaxLogBatch is the most lines one BuildLog message may carry.
	MaxLogBatch = 500
	// MaxReportBuilds is the most statuses one BuildReport may carry.
	MaxReportBuilds = 200
	// BuildResendWithin: a build is resent to its cluster (on reconnects and
	// resyncs) only this long after it was dispatched. Later its deadline
	// has passed, and an agent that already dropped it must not build it again.
	BuildResendWithin = MaxBuildTimeout + 10*time.Minute
)

// CodeNoBuilder: the environment's cluster announced no builder.
const CodeNoBuilder = "NO_BUILDER"

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// DesiredBuild is a build sent to the cluster of its environment: what its
// agent turns into an OdysseyBuild. The build domain decides it (buildfleet);
// the fleet places it, carries it and keeps what the agent reports.
type DesiredBuild struct {
	ID           BuildID                 `db:"id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Project      project.ID              `db:"project_id"`
	Environment  project.EnvironmentID   `db:"environment_id"`
	// Cluster is set by placement (the environment's cluster).
	Cluster ClusterID `db:"cluster_id"`
	// SourceKey is where the gateway reads the source from (blob storage).
	SourceKey    string `db:"source_key"`
	SourceSHA256 string `db:"source_sha256"`
	SourceSize   int64  `db:"source_size"`
	// ImageRepository and CacheRef are derived from the cluster's builder.
	ImageRepository string        `db:"image_repository"`
	CacheRef        string        `db:"cache_ref"`
	Timeout         time.Duration `db:"-"`
	// Agent says how the cluster is told about a build: Build while it is
	// wanted, CancelBuild once it is not (see Wanted).
	Cancelled bool      `db:"cancelled"`
	CreatedAt time.Time `db:"created_at"`
	// Final: the agent reported it succeeded or failed (it is not sent again:
	// an agent that already dropped it must not build it twice).
	Final bool `db:"final"`
}

// Wanted reports whether the cluster should be (still) building it at now.
func (b DesiredBuild) Wanted(now time.Time) bool {
	return !b.Cancelled && !b.Final && now.Sub(b.CreatedAt) < BuildResendWithin
}

// Validate checks what the build domain hands over. The source must be one
// of the organization's (the gateway streams whatever key is stored).
func (b DesiredBuild) Validate() error {
	switch {
	case b.ID.IsZero() || b.Organization.IsZero() || b.Project.IsZero() || b.Environment.IsZero():
		return errx.Validation("a build needs an id, organization, project and environment")
	case blob.ValidateKey(b.SourceKey) != nil || !strings.HasPrefix(b.SourceKey, blob.TenantKey(b.Organization, "sources")+"/"):
		return errx.Validation("a build needs the key of one of its organization's sources")
	case !sha256Pattern.MatchString(b.SourceSHA256) || b.SourceSize <= 0:
		return errx.Validation("a build needs its source's sha256 and size")
	case b.Timeout < time.Minute || b.Timeout > MaxBuildTimeout:
		return errx.Validation("a build's timeout must be between 1 minute and 1 hour")
	}
	return nil
}

// BuildPhase is how the agent sees a build.
type BuildPhase string

const (
	BuildPending   BuildPhase = "pending"
	BuildRunning   BuildPhase = "running"
	BuildSucceeded BuildPhase = "succeeded"
	BuildFailed    BuildPhase = "failed"
)

func (p BuildPhase) Valid() bool {
	return p == BuildPending || p == BuildRunning || p == BuildSucceeded || p == BuildFailed
}

// Final phases never change once reported.
func (p BuildPhase) Final() bool { return p == BuildSucceeded || p == BuildFailed }

// Reasons an agent gives for a failed build (the build domain's Reason* values).
var buildReasons = map[string]bool{"": true, "unsupported": true, "source": true, "push": true, "timeout": true, "failed": true}

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// BuildStatus is what the agent last reported of a build.
type BuildStatus struct {
	Build BuildID    `db:"build_id"`
	Phase BuildPhase `db:"phase"`
	// Digest is the pushed image's digest (succeeded only).
	Digest  string `db:"image_digest"`
	Reason  string `db:"reason"`
	Message string `db:"message"`
	// Image is ImageRepository@Digest (succeeded only; BuildStatus fills it).
	Image      string    `db:"-"`
	ObservedAt time.Time `db:"observed_at"`
}

// Validate bounds what an agent reports of a build. Its message and reason
// are not judged here: Clean makes them safe (a failure's message is often
// a builder's coloured output, and losing a final report would leave the
// build hanging until its timeout).
func (s BuildStatus) Validate() error {
	switch {
	case s.Build.IsZero():
		return errx.Validation("reported build needs its id")
	case !s.Phase.Valid():
		return errx.Validation("reported build phase is not valid")
	case s.Phase == BuildSucceeded && !digestPattern.MatchString(s.Digest):
		return errx.Validation("a succeeded build must report its image digest")
	case s.Phase != BuildSucceeded && s.Digest != "":
		return errx.Validation("only a succeeded build reports an image digest")
	}
	return nil
}

// Clean is the status with its message made safe to store and show (like a
// log line, at most MaxMessage runes) and an unknown reason of a failed
// build replaced by "failed" (no reason at all for the other phases).
func (s BuildStatus) Clean() BuildStatus {
	s.Message = cleanText(s.Message)
	if r := []rune(s.Message); len(r) > MaxMessage {
		s.Message = string(r[:MaxMessage-1]) + "…"
	}
	switch {
	case s.Phase != BuildFailed:
		s.Reason = ""
	case s.Reason == "" || !buildReasons[s.Reason]:
		s.Reason = "failed"
	}
	return s
}

// LogLine is one line a build printed.
type LogLine struct {
	Seq    int64  `db:"seq"`
	Stream string `db:"stream"`
	Text   string `db:"text"`
}

var logStreams = map[string]bool{"prepare": true, "build": true, "system": true}

// MaxLogSeq bounds a line's seq (the agent numbers lines per container).
const MaxLogSeq = 1 << 40

// Clean makes a reported line safe to store and show: valid UTF-8 without
// control characters other than tabs (terminal escapes included), at most
// MaxLogLineBytes. It reports false for a line that cannot be kept.
func (l LogLine) Clean() (LogLine, bool) {
	if l.Seq <= 0 || l.Seq > MaxLogSeq || !logStreams[l.Stream] {
		return l, false
	}
	t := cleanText(l.Text)
	if len(t) > MaxLogLineBytes {
		t = strings.ToValidUTF8(t[:MaxLogLineBytes-len("…")], "") + "…"
	}
	l.Text = t
	return l, true
}

// cleanText is valid UTF-8 without terminal escapes or control characters
// other than tabs.
func cleanText(s string) string {
	t := strings.ToValidUTF8(s, "\uFFFD")
	t = stripEscapes(t)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return -1
		}
		return r
	}, t)
}

// ansi matches terminal escape sequences (colours, cursor moves).
var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

func stripEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	return ansi.ReplaceAllString(s, "")
}

// BuildLog is a batch of lines of one build from its agent.
type BuildLog struct {
	Build BuildID
	Lines []LogLine
}

// Validate bounds the batch; Clean drops the lines that cannot be kept.
func (b BuildLog) Validate() error {
	if b.Build.IsZero() {
		return errx.Validation("a build log needs its build id")
	}
	if len(b.Lines) > MaxLogBatch {
		return errx.Validation("too many lines in one build log message")
	}
	return nil
}

// Clean is the batch with every line cleaned and the unusable ones dropped.
func (b BuildLog) Clean() BuildLog {
	out := BuildLog{Build: b.Build, Lines: make([]LogLine, 0, len(b.Lines))}
	for _, l := range b.Lines {
		if c, ok := l.Clean(); ok {
			out.Lines = append(out.Lines, c)
		}
	}
	return out
}

// LogPage is the stored lines of a build after a seq.
type LogPage struct {
	Lines []LogLine
	// Truncated: the log reached MaxBuildLogBytes and later lines were dropped.
	Truncated bool
	// LastSeq is the highest seq stored.
	LastSeq int64
}

// LogCutText ends a truncated log.
const LogCutText = "(log cortado: la construcción imprimió demasiado)"
