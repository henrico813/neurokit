package manifest_test

import (
	"strings"
	"testing"

	"github.com/henrico813/neurokit/internal/manifest"
)

// Handlers use their arguments without checking them, so a request with a
// missing, unknown or out-of-range argument must be turned away before a
// handler runs. The valid request keeps its approval label, because the broker
// writes it to the audit log.
func TestLooseArgumentsAreRejected(t *testing.T) {
	m, err := manifest.ReadAndValidate(writeManifest(t, validManifest))
	if err != nil {
		t.Fatal(err)
	}
	swap := m.Commands["swap"]
	label := strings.Repeat("a", 200)

	args, err := swap.DecodeAndValidateArgs([]byte(`{"player":"ÑoñoÑoñoÑo","slot":3,"tags":["a"],"move":{"to":"bench"},"approval":"` + label + `"}`))
	if err != nil || args["approval"] != label {
		t.Fatalf("valid body: args %v, error %v; want no error and the approval label kept", args, err)
	}

	tests := []struct {
		name string
		body string
		want string
	}{
		{"unknown argument", `{"player":"kim","slot":3,"tags":[],"plaer":"x"}`, "plaer"},
		{"missing argument", `{"player":"kim","tags":[]}`, "slot"},
		{"fraction for an integer", `{"player":"kim","slot":1.5,"tags":[]}`, "slot"},
		{"string over max_length", `{"player":"kimberlyann","slot":3,"tags":[]}`, "player"},
		{"list over max_items", `{"player":"kim","slot":3,"tags":["a","b","c"]}`, "tags"},
		{"list item over max_length", `{"player":"kim","slot":3,"tags":["abcdef"]}`, "tags"},
		{"integer over max", `{"player":"kim","slot":10,"tags":[]}`, "slot"},
		{"over-long approval", `{"player":"kim","slot":3,"tags":[],"approval":"` + label + `a"}`, "approval"},
		{"unknown field in an object", `{"player":"kim","slot":3,"tags":[],"move":{"to":"bench","from":"x"}}`, "from"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := swap.DecodeAndValidateArgs([]byte(tt.body))

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("DecodeAndValidateArgs(%q) error = %v; want one containing %q", tt.body, err, tt.want)
			}
		})
	}
}
