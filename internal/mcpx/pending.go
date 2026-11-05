package mcpx

import (
	"reflect"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/gate"
)

// Pending is what a tool whose action IT's rules hold for approval embeds in
// its result: not an error (nothing failed, the action waits for a person), so
// the assistant tells the user and moves on. The other fields of the result
// stay empty: the action did NOT happen.
type Pending struct {
	Status    string `json:"status,omitempty" jsonschema:"pending_approval cuando la acción quedó esperando la aprobación de TI (no se hizo nada todavía)"`
	RequestID string `json:"request_id,omitempty" jsonschema:"la solicitud de aprobación; get_request muestra cómo va"`
}

// PendingOf is the Pending of a held action.
func PendingOf(p *gate.Pending) Pending {
	return Pending{Status: gate.StatusPendingApproval, RequestID: p.RequestID}
}

// PendingStep is the next_step of a held action: say it plainly, do not retry,
// and follow it with get_request.
func PendingStep(p *gate.Pending) string {
	return "Esto necesita la aprobación de TI y ya se la pedimos (request_id=" + p.RequestID + "): " +
		"dile al usuario que quedó pendiente, que no se hizo nada todavía y que le avisaremos cuando lo decidan. " +
		"No lo reintentes ni busques otra vía; get_request (request_id) muestra cómo va."
}

// PendingConflicts lists, for a tool's output type, every JSON name that an
// embedded Pending shares with another field of the result. encoding/json
// lets the shallower field win, so a held result would then serialise the
// wrong (usually empty) `status`: the output must either embed Pending and
// declare neither `status` nor `request_id` itself, or not embed it. Empty
// when there is no conflict (or no Pending).
func PendingConflicts(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	type entry struct {
		name    string
		pending bool
	}
	var all []entry
	var walk func(t reflect.Type, inPending bool)
	walk = func(t reflect.Type, inPending bool) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
				walk(ft, inPending || ft == reflect.TypeOf(Pending{}))
				continue
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			all = append(all, entry{name, inPending})
		}
	}
	walk(t, false)
	var out []string
	for _, p := range all {
		if !p.pending {
			continue
		}
		for _, o := range all {
			if !o.pending && o.name == p.name {
				out = append(out, p.name)
				break
			}
		}
	}
	return out
}
