package internal

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const internalPath = "github.com/henrico813/neurokit/internal/"

// allowedImports lists the internal packages each internal package may
// import. go test runs in internal/, so "./..." never reaches cmd/neurokit,
// which may import anything.
var allowedImports = map[string][]string{
	"wire":     {},
	"manifest": {"wire"},
	"runner":   {},
	"broker": {
		"wire",
		"manifest",
		"runner",
	},
	"cli": {
		"wire",
		"manifest",
	},
	"conform": {
		"wire",
		"manifest",
	},
	"check": {},
}

// The client must not reach into server internals, and runner must not
// depend on the wire format. A new package fails until it has a row, so its
// place is decided on purpose. Test-only imports are not checked.
func TestImportDirection(t *testing.T) {
	list := exec.Command(
		"go",
		"list",
		"-f",
		`{{if .GoFiles}}{{.ImportPath}} {{join .Imports " "}}{{end}}`,
		"./...",
	)
	var stderr strings.Builder
	list.Stderr = &stderr

	out,
		err := list.Output()
	if err != nil {
		t.Fatalf(
			"go list: %v\n%s",
			err,
			stderr.String(),
		)
	}

	for _, line := range strings.Split(
		string(out),
		"\n",
	) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		concern := strings.TrimPrefix(
			fields[0],
			internalPath,
		)
		allowed,
			ok := allowedImports[concern]
		if !ok {
			t.Errorf(
				"%s has no row in allowedImports; add one",
				fields[0],
			)
			continue
		}
		for _, imported := range fields[1:] {
			target,
				isInternal := strings.CutPrefix(
				imported,
				internalPath,
			)
			if isInternal && !slices.Contains(
				allowed,
				target,
			) {
				t.Errorf(
					"%s imports %s, which its row does not allow",
					concern,
					target,
				)
			}
		}
	}
}
