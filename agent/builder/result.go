// Package builder is what runs inside a build's Job (`odyssey-agent builder
// prepare|build`), and the contract between it and the agent's build
// controller: each container ends by writing a Result to its termination
// message, which the controller reads from the Pod's status.
//
//   - prepare (init container, the agent's image): fetches the source from
//     the agent, verifies and extracts it (pkg/srcarchive), and decides how to
//     build it: the app's Dockerfile, or a Railpack plan.
//   - build (main container, BuildKit rootless): builds with buildctl and
//     pushes the image, reporting its digest.
package builder

import (
	"encoding/json"
	"os"
	"strings"
	"unicode/utf8"
)

// Reasons of a failed build (the fleet protocol's).
const (
	ReasonUnsupported = "unsupported"
	ReasonSource      = "source"
	ReasonPush        = "push"
	ReasonFailed      = "failed"
)

// Result is what a builder container reports: Digest when it pushed the
// image, Reason and Message when it failed. Reused: the Digest is an earlier
// build's image, which this build only tagged.
type Result struct {
	Digest  string `json:"digest,omitempty"`
	Reused  bool   `json:"reused,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// TerminationLog is where Kubernetes reads a container's termination
// message (at most 4096 bytes).
const TerminationLog = "/dev/termination-log"

// maxMessage keeps the encoded Result under the 4096 bytes Kubernetes keeps
// (JSON may escape a rune into 6 bytes, plus the other fields).
const maxMessage = 600

// Encode is the termination message of r: its message shortened to its end
// (where a failing tool explains itself).
func (r Result) Encode() []byte {
	r.Message = Tail(r.Message, maxMessage)
	b, _ := json.Marshal(r)
	return b
}

// ParseResult reads a termination message; ok is false when it is not a
// Result (empty, or Kubernetes' own words).
func ParseResult(msg string) (Result, bool) {
	var r Result
	if strings.TrimSpace(msg) == "" || json.Unmarshal([]byte(msg), &r) != nil {
		return Result{}, false
	}
	return r, r.Digest != "" || r.Reason != "" || r.Message != ""
}

// Tail is the end of s, at most n runes, on a line boundary when one is near.
func Tail(s string, n int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, "?"))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	s = string(r[len(r)-n+1:])
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)/4 {
		s = s[i+1:]
	}
	return "…" + s
}

func writeResult(path string, r Result) error {
	return os.WriteFile(path, r.Encode(), 0o644)
}
