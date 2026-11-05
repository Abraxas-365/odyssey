// Package inventory is the read-only view of everything the organization
// runs (cut 3c): "Inventario" for TI, and "Necesita tu atención" for
// everyone. It owns no table and writes nothing: it composes what the other
// domains already know (apps, what is online, scans, clusters, requests)
// through the narrow ports of ports.go, so no domain learns of it
// (docs/inventory.md, architecture test TestInventoryIsReadOnly).
package inventory

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Limits of one inventory read. The apps are read in batches (one query per
// kind of data, never one per app); only the "needs attention" filter and the
// CSV ask the release domain for each environment's uses, with a few at a
// time (UsesWorkers), so they stop at MaxApps.
const (
	// MaxApps is how many apps one read looks at; more is reported in
	// Page.Truncated (narrow the filters).
	MaxApps = 500
	// UsesWorkers is how many environments' uses are read at once.
	UsesWorkers = 8
	// FailedWithin is how recent a failed deployment must be to need attention.
	FailedWithin = 24 * time.Hour
	// RejectedWithin is how long a rejected request stays on its owner's list.
	RejectedWithin = 7 * 24 * time.Hour
	// MaxItems bounds each source of the attention list.
	MaxItems = 100
)

// Status is where an environment stands.
type Status string

const (
	StatusNotDeployed Status = "not_deployed"
	StatusDeploying   Status = "deploying"
	StatusOnline      Status = "online"
	StatusFailed      Status = "failed"
)

// ScanState is what is known about the online image's vulnerabilities.
type ScanState string

const (
	// ScanNone: nothing built here is online (no deployment yet, or an image
	// published as it was: it is never scanned).
	ScanNone ScanState = "none"
	// ScanPending: built, and the scan has not been recorded yet.
	ScanPending ScanState = "pending"
	// ScanUnknown: the scan could not be done (Scan.Error says why).
	ScanUnknown ScanState = "unknown"
	// ScanClean: no critical or high vulnerabilities.
	ScanClean ScanState = "clean"
	// ScanVulnerable: critical or high vulnerabilities.
	ScanVulnerable ScanState = "vulnerable"
)

// Scan is the last scan of the image an environment serves.
type Scan struct {
	State    ScanState `json:"state"`
	Critical int       `json:"critical"`
	High     int       `json:"high"`
	// Error is why it could not be done, in plain Spanish (state unknown).
	Error     string     `json:"error,omitempty"`
	ScannedAt *time.Time `json:"scanned_at"`
}

// Row is one environment of one app.
type Row struct {
	App         AppRef         `json:"app"`
	Environment EnvironmentRef `json:"environment"`
	// Owner is who created the app.
	Owner  identity.UserID    `json:"owner"`
	Access release.AccessMode `json:"access"`
	URL    string             `json:"url"`
	Status Status             `json:"status"`
	// Online is the version being served (nil when nothing is).
	Online    *Online        `json:"online"`
	Resources []ResourceRef  `json:"resources"`
	AI        bool           `json:"ai"`
	Secrets   SecretsSummary `json:"secrets"`
	Scan      Scan           `json:"scan"`
	Cluster   *ClusterRef    `json:"cluster"`
	// NeedsAttention: someone has something to do (see Attention).
	NeedsAttention bool           `json:"needs_attention"`
	Attention      []AppAttention `json:"attention"`
	// Unavailable are the sections of the uses that could not be read now
	// (resources, secrets, ai, egress): their fields are then incomplete.
	Unavailable []string `json:"unavailable"`
}

type AppRef struct {
	ID   project.ID   `json:"id"`
	Slug string       `json:"slug"`
	Name string       `json:"name"`
	Tier project.Tier `json:"tier"`
}

type EnvironmentRef struct {
	ID   project.EnvironmentID   `json:"id"`
	Slug string                  `json:"slug"`
	Kind project.EnvironmentKind `json:"kind"`
}

type ClusterRef struct {
	ID   fleet.ClusterID `json:"id"`
	Name string          `json:"name"`
}

// Online is the release being served.
type Online struct {
	Image string     `json:"image"`
	Since *time.Time `json:"since"`
}

type ResourceRef struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type SecretsSummary struct {
	Total   int      `json:"total"`
	Missing []string `json:"missing"`
}

// AppAttention is one thing an app's people (or TI) must do, as the release
// domain says it.
type AppAttention struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Who is who can act: owner, it or wait (release.AttentionWho).
	Who string `json:"who"`
}

// Filter narrows the inventory. The zero value matches everything.
type Filter struct {
	Tier    []project.Tier
	Cluster fleet.ClusterID
	// NeedsAttention keeps only the environments that need it.
	NeedsAttention bool
	// Search matches an app's name or slug.
	Search string
}

func (f Filter) Validate() error {
	for _, t := range f.Tier {
		if !t.Valid() {
			return errx.InvalidField("tier", project.TierMessage)
		}
	}
	return nil
}

// Page is a page of the inventory: the standard {items, page} envelope plus
// Truncated and Unavailable.
type Page struct {
	Items []Row      `json:"items"`
	Page  query.Page `json:"page"`
	// Truncated: the organization has more apps than one read looks at
	// (MaxApps); the filters narrow it.
	Truncated bool `json:"truncated"`
	// Unavailable are the sources the filter needed and that could not be
	// read now (today only "clusters", with ?cluster=): the items are then
	// incomplete, never an empty list that claims nothing matches.
	Unavailable []Source `json:"unavailable"`
}

// Kind is the closed list of things the attention list says.
type Kind string

const (
	KindApprovalPending     Kind = "approval_pending"     // TI: a request waits for their decision
	KindAppVulnerable       Kind = "app_vulnerable"       // TI: an online app has critical vulnerabilities
	KindClusterDisconnected Kind = "cluster_disconnected" // TI: a cluster does not answer
	KindClusterPolicy       Kind = "cluster_policy"       // TI: a cluster does not apply its network policy
	KindDeploymentFailed    Kind = "deployment_failed"    // TI: an app's last publication failed (24 h)
	KindAppAttention        Kind = "app_attention"        // mine: something an app needs from its people
	KindRequestPending      Kind = "request_pending"      // mine: my request waits for TI
	KindRequestRejected     Kind = "request_rejected"     // mine: TI rejected my request
	KindRequestFailed       Kind = "request_failed"       // mine: my request was approved but could not run
)

// Kinds are every kind, in the order the list shows them.
var Kinds = []Kind{KindApprovalPending, KindAppVulnerable, KindClusterDisconnected, KindClusterPolicy, KindDeploymentFailed,
	KindAppAttention, KindRequestPending, KindRequestRejected, KindRequestFailed}

// Scope says whose list an item is on.
type Scope string

const (
	ScopeIT   Scope = "it"   // what TI watches over the whole organization
	ScopeMine Scope = "mine" // what the viewer's own apps and requests need
)

// Severity orders the list.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Source names where attention items come from, for Unavailable.
type Source string

const (
	SourceApprovals Source = "approvals"
	SourceApps      Source = "apps"
	SourceClusters  Source = "clusters"
	SourceRequests  Source = "requests"
)

// Item is one thing that needs attention.
type Item struct {
	Kind     Kind     `json:"kind"`
	Scope    Scope    `json:"scope"`
	Severity Severity `json:"severity"`
	// Sentence says it in plain Spanish.
	Sentence string `json:"sentence"`
	// Link is the console path where it is solved ("/apps/<id>").
	Link string `json:"link"`
	// Detail refines KindAppAttention: the release.AttentionKind.
	Detail      string                 `json:"detail,omitempty"`
	App         *project.ID            `json:"app_id"`
	Environment *project.EnvironmentID `json:"environment_id"`
	Cluster     *fleet.ClusterID       `json:"cluster_id"`
	Request     string                 `json:"request_id,omitempty"`
	At          *time.Time             `json:"at"`
}

// Attention is the list "Necesita tu atención".
type Attention struct {
	Items []Item `json:"items"`
	// Unavailable are the sources that could not be read now: the list is
	// then incomplete (never wrongly empty without saying so).
	Unavailable []Source `json:"unavailable"`
	// Truncated: a source had more than MaxItems.
	Truncated bool `json:"truncated"`
}
