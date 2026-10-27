package errx

// GenericMessage is what every 5xx body says; the cause only goes to the log.
const GenericMessage = "an unexpected error occurred"

// Response is the wire shape of an error (docs/api.md §3), sent as
// {"error": Response}. StatusCode repeats the HTTP status for clients that
// only parse the body; RequestID is the response's X-Request-ID.
type Response struct {
	Type       string                 `json:"type"`
	Code       string                 `json:"code"`
	Message    string                 `json:"message"`
	StatusCode int                    `json:"status_code"`
	Details    map[string]interface{} `json:"details,omitempty"`
	RequestID  string                 `json:"request_id"`
}

// ToResponse builds the wire shape. 5xx errors are reduced to a generic
// message and no details, so wrapped operation names and causes never leak.
func (e *Error) ToResponse(requestID string) Response {
	r := Response{
		Type: string(e.Type), Code: e.Code, Message: e.Message,
		StatusCode: e.HTTPStatus, Details: e.Details, RequestID: requestID,
	}
	if e.HTTPStatus >= 500 {
		r.Message, r.Details = GenericMessage, nil
	}
	return r
}
