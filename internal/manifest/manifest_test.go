package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henrico813/neurokit/internal/manifest"
)

// Each test case replaces one snippet of validManifest, so a snippet must
// appear only once.
const validManifest = `{
  "name": "demo",
  "spec": 1,
  "broker_url_env": "DEMO_BROKER_URL",
  "max_handlers": 2,
  "program": ["python", "-m", "neurokit_handler", "demo.handler"],
  "check": ["python", "-m", "neurokit_handler", "--check", "demo.handler"],
  "commands": {
    "lookup": {
      "max_body_bytes": 1024,
      "max_response_bytes": 4096,
      "deadline_ms": 1000,
      "args": {"query": {"type": "string", "max_length": 100}}
    },
    "swap": {
      "mutating": true,
      "lock": "write",
      "approval": true,
      "max_body_bytes": 2048,
      "max_response_bytes": 8192,
      "deadline_ms": 2000,
      "args": {
        "player": {"type": "string", "max_length": 10},
        "slot": {"type": "integer", "min": 1, "max": 9},
        "tags": {"type": "list", "items": {"type": "string", "max_length": 5}, "max_items": 2},
        "move": {"type": "object", "optional": true, "fields": {"to": {"type": "string", "max_length": 5}}}
      },
      "errors": ["lineup-locked"]
    }
  }
}`

func writeManifest(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A manifest that passes is served as written, so each mistake here must stop
// serve at start, with one line saying why. Both have happened: two existing
// brokers set no request size limit, and a typo like "mutatng" would run a
// change as a read.
func TestUnsafeManifestsAreRejected(t *testing.T) {
	if _, err := manifest.ReadAndValidate(writeManifest(t, validManifest)); err != nil {
		t.Fatalf("the valid manifest must parse: %v", err)
	}
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{name: "missing body limit", replace: `"max_body_bytes": 1024,`, with: ``, want: "max_body_bytes"},
		{name: "missing response limit", replace: `"max_response_bytes": 4096,`, with: ``, want: "max_response_bytes"},
		{name: "missing deadline", replace: `"deadline_ms": 1000,`, with: ``, want: "deadline_ms"},
		{name: "missing max_handlers", replace: `"max_handlers": 2,`, with: ``, want: "max_handlers"},
		{name: "missing check", replace: `"check": ["python", "-m", "neurokit_handler", "--check", "demo.handler"],`, with: ``, want: "check is required"},
		{name: "python ready program", replace: `"max_handlers": 2,`, with: `"max_handlers": 2, "ready": ["/usr/local/bin/python3.12", "-c", "pass"],`, want: "ready"},
		{name: "newer spec with a new field", replace: `"spec": 1,`, with: `"spec": 2, "per_command_program": true,`, want: "spec 2"},
		// Only the CLI sends broker-unavailable, never the broker, so a tool
		// author is the most likely to forget the code is taken.
		{name: "declared code shadows shared", replace: `"errors": ["lineup-locked"]`, with: `"errors": ["broker-unavailable"]`, want: "broker-unavailable"},
		{name: "argument named approval", replace: `"player": {`, with: `"approval": {`, want: `"approval" is reserved`},
		{name: "approval on a read", replace: `"lookup": {`, with: `"lookup": {"approval": true,`, want: "only on a mutating"},
		{name: "command named check", replace: `"lookup": {`, with: `"check": {`, want: `"check" is reserved`},
		{name: "misspelled field", replace: `"mutating": true,`, with: `"mutatng": true,`, want: "mutatng"},
		{name: "unknown lock value", replace: `"lock": "write",`, with: `"lock": "Write",`, want: "lock"},
		{name: "integer without max", replace: `"min": 1, "max": 9}`, with: `"min": 1}`, want: "integer needs min and max"},
		{name: "unknown argument type", replace: `"type": "string", "max_length": 100`, with: `"type": "str", "max_length": 100`, want: `"str"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeManifest(t, strings.Replace(validManifest, tt.replace, tt.with, 1))

			_, err := manifest.ReadAndValidate(path)

			if err == nil {
				t.Fatalf("ReadAndValidate accepted the manifest; want an error containing %q", tt.want)
			}
			// Remove the temp path first: it contains the case name, such as
			// missing_max_handlers, which could match want on its own.
			reason := strings.ReplaceAll(err.Error(), path, "")
			if !strings.Contains(reason, tt.want) || strings.Contains(reason, "\n") {
				t.Errorf("ReadAndValidate error = %v; want one line containing %q", err, tt.want)
			}
		})
	}
}
