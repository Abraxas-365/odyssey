package httpx

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/gate"
)

// Held answers an action IT's rules hold for approval: 202 with the pending
// answer ({status: "pending_approval", request_id, message}), nothing else of
// the action's own result. It reports whether it answered; the handler then
// returns its error:
//
//	if done, err := httpx.Held(w, out.Outcome); done {
//		return err
//	}
//
// Every governed action (set_access, enable_ai, connect, delete, create with a
// connection) answers the same way, so the console and the agents read one shape.
func Held(w http.ResponseWriter, o gate.Outcome) (bool, error) {
	if !o.Held() {
		return false, nil
	}
	return true, JSONStatus(w, http.StatusAccepted, o.Pending)
}
