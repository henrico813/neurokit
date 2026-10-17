package manifest_test

import (
	"strings"
	"testing"

	"github.com/henrico813/neurokit/internal/manifest"
)

// Handlers use their arguments without checking them, so a request with a
// missing, unknown or out-of-range argument must be turned away before a
// handler runs. The valid request's approval label comes back apart from its
// arguments, because the audit log needs it and the handler must never see it.
func TestLooseArgumentsAreRejected(t *testing.T) {
	m, _, err := manifest.ReadAndValidate(writeManifest(t, validManifest))
	if err != nil {
		t.Fatal(err)
	}
	swap := m.Commands["swap"]
	label := strings.Repeat("a", 200)

	args, gotLabel, err := swap.DecodeAndValidateArgs(
		[]byte(`{"player":"ÑoñoÑoñoÑo","slot":3,"tags":["a"],"move":{"to":"bench"},"approval":"` + label + `"}`),
	)
	if _, kept := args["approval"]; err != nil || gotLabel != label || kept {
		t.Fatalf(
			"valid body: args %v, label %q, error %v; want no error, and the label returned apart from the arguments",
			args,
			gotLabel,
			err,
		)
	}

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown argument",
			body: `{"player":"kim","slot":3,"tags":[],"plaer":"x"}`,
			want: "plaer",
		},
		{
			name: "missing argument",
			body: `{"player":"kim","tags":[]}`,
			want: "slot",
		},
		{
			name: "fraction for an integer",
			body: `{"player":"kim","slot":1.5,"tags":[]}`,
			want: "slot",
		},
		{
			name: "string over max_length",
			body: `{"player":"kimberlyann","slot":3,"tags":[]}`,
			want: "player",
		},
		{
			name: "list over max_items",
			body: `{"player":"kim","slot":3,"tags":["a","b","c"]}`,
			want: "tags",
		},
		{
			name: "list item over max_length",
			body: `{"player":"kim","slot":3,"tags":["abcdef"]}`,
			want: "tags",
		},
		{
			name: "integer over max",
			body: `{"player":"kim","slot":10,"tags":[]}`,
			want: "slot",
		},
		{
			name: "over-long approval",
			body: `{"player":"kim","slot":3,"tags":[],"approval":"` + label + `a"}`,
			want: "approval",
		},
		{
			name: "unknown field in an object",
			body: `{"player":"kim","slot":3,"tags":[],"move":{"to":"bench","from":"x"}}`,
			want: "from",
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				_, _, err := swap.DecodeAndValidateArgs([]byte(tt.body))

				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Errorf(
						"DecodeAndValidateArgs(%q) error = %v; want one containing %q",
						tt.body,
						err,
						tt.want,
					)
				}
			},
		)
	}
}
