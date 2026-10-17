package wire

import "encoding/json"

const (
	SpecHeader         = "Neurokit-Spec"
	CommandRoutePrefix = "/commands/"
	LivezRoute         = "/livez"
	ReadyzRoute        = "/readyz"
	ManifestRoute      = "/manifest"
)

// The body of every broker reply and handler answer: Result on success, Error
// otherwise, and Write for a mutating command.
type Reply struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ErrorDetail    `json:"error,omitempty"`
	Write  WriteState      `json:"write,omitempty"`
}
