// Package manifest reads a tool's manifest.json, the one place a tool
// declares its commands, and checks each request's arguments against it.
// ReadAndValidate rejects a manifest that would let a command run without
// limits or collide with a reserved name, so serve can stop at start with a
// one-line reason.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/henrico813/neurokit/internal/wire"
)

// The only manifest and wire version this binary knows. It changes whenever
// the manifest format or the HTTP wire changes.
const Spec = 1

const maxManifestBytes = 1 << 20

var (
	commandName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// Handlers get each argument as a Python parameter, and the CLI turns it
	// into a flag, such as slot_id into --slot-id, so a name must work as
	// both.
	argName  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	codeName = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)
)

type Manifest struct {
	Name         string              `json:"name"`
	Spec         int                 `json:"spec"`
	BrokerURLEnv string              `json:"broker_url_env"`
	MaxHandlers  int                 `json:"max_handlers"`
	Program      []string            `json:"program"`
	Check        []string            `json:"check"`
	Ready        []string            `json:"ready"`
	Commands     map[string]*Command `json:"commands"`
}

type Command struct {
	Mutating         bool           `json:"mutating"`
	Lock             string         `json:"lock"`
	Approval         bool           `json:"approval"`
	MaxBodyBytes     int64          `json:"max_body_bytes"`
	MaxResponseBytes int64          `json:"max_response_bytes"`
	DeadlineMS       int64          `json:"deadline_ms"`
	Args             map[string]Arg `json:"args"`
	Errors           []string       `json:"errors"`
}

type Arg struct {
	Type      string         `json:"type"`
	Optional  bool           `json:"optional"`
	MaxLength int            `json:"max_length"`
	Min       *int64         `json:"min"`
	Max       *int64         `json:"max"`
	Items     *Arg           `json:"items"`
	MaxItems  int            `json:"max_items"`
	Fields    map[string]Arg `json:"fields"`
}

// Returns the manifest at path, and the bytes it read, only if every command
// has its limits and no name is reserved. Otherwise returns the first problem
// as one line, such as `manifest /opt/demo/manifest.json: command "lookup":
// deadline_ms must be positive`.
func ReadAndValidate(path string) (*Manifest, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, nil, err
	}
	m, err := ParseAndValidate(data)
	if err != nil {
		return nil, nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return m, data, nil
}

// Returns the manifest in data only if every command has its limits and no
// name is reserved. Errors do not name a file; ReadAndValidate adds the path.
func ParseAndValidate(data []byte) (*Manifest, error) {
	// Check spec before strict decoding. A newer spec usually adds a field,
	// and "unknown field" would hide the real reason.
	var version struct {
		Spec int `json:"spec"`
	}
	if json.Unmarshal(data, &version) == nil && version.Spec != Spec {
		return nil, fmt.Errorf(
			"spec %d is not supported; this binary knows spec %d",
			version.Spec,
			Spec,
		)
	}
	var m Manifest
	err := wire.DecodeOneJSONValue(bytes.NewReader(data), maxManifestBytes, &m)
	if err == nil {
		err = m.validate()
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		err = fmt.Errorf(
			"field %q must be %s, not %s",
			typeErr.Field,
			typeErr.Type,
			typeErr.Value,
		)
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	switch {
	case m.Name == "":
		return errors.New("name is required")
	case m.BrokerURLEnv == "":
		return errors.New("broker_url_env is required")
	case m.MaxHandlers <= 0:
		return errors.New("max_handlers must be positive")
	case len(m.Program) == 0:
		return errors.New("program is required")
	case len(m.Check) == 0:
		return errors.New("check is required")
	case len(m.Commands) == 0:
		return errors.New("commands must not be empty")
	}
	// A Python ready program starts an interpreter on every health check.
	// Health checks run every 5 s, so for the scout tool that is about 12%
	// of a core. The name test catches the common mistake, not every slow
	// program.
	if len(m.Ready) > 0 && strings.HasPrefix(
		filepath.Base(m.Ready[0]),
		"python",
	) {
		return fmt.Errorf(
			"ready must be cheap, such as a file test, not %q",
			m.Ready[0],
		)
	}
	for _, name := range slices.Sorted(maps.Keys(m.Commands)) {
		if err := validateCommand(name, m.Commands[name]); err != nil {
			return fmt.Errorf("command %q: %w", name, err)
		}
	}
	return nil
}

func validateCommand(name string, c *Command) error {
	switch {
	case c == nil:
		return errors.New("must be an object")
	case !commandName.MatchString(name):
		return errors.New("name must be lowercase letters, digits and hyphens")
	case name == "check":
		// Each command is a function in the tool's Python module, and the
		// module's check() is the startup check, so no command may be named
		// check.
		return errors.New(`name "check" is reserved for the startup check`)
	case c.MaxBodyBytes <= 0:
		return errors.New("max_body_bytes must be positive")
	case c.MaxResponseBytes <= 0:
		return errors.New("max_response_bytes must be positive")
	case c.DeadlineMS <= 0:
		return errors.New("deadline_ms must be positive")
	case c.Approval && !c.Mutating:
		return errors.New("approval is allowed only on a mutating command")
	}
	if c.Lock != "" && c.Lock != "read" && c.Lock != "write" {
		return fmt.Errorf("lock must be read or write, not %q", c.Lock)
	}
	for _, code := range c.Errors {
		if !codeName.MatchString(code) || wire.IsSharedCode(code) {
			return fmt.Errorf("cannot declare error code %q", code)
		}
	}
	if _, ok := c.Args["approval"]; ok {
		return errors.New(`argument name "approval" is reserved for the audit label`)
	}
	for _, name := range []string{"help", "h"} {
		if _, ok := c.Args[name]; ok {
			return fmt.Errorf("argument name %q is reserved for neurokit run --help", name)
		}
	}
	return validateDeclaredArgs(c.Args)
}

func validateDeclaredArgs(fields map[string]Arg) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !argName.MatchString(name) {
			return fmt.Errorf(
				"argument %q: name must be lowercase letters, digits and underscores",
				name,
			)
		}
		if err := validateArgTypeAndBounds(fields[name]); err != nil {
			return fmt.Errorf("argument %s: %w", name, err)
		}
	}
	return nil
}

func validateArgTypeAndBounds(a Arg) error {
	switch a.Type {
	case "string":
		if a.MaxLength <= 0 {
			return errors.New("string needs a positive max_length")
		}
	case "integer":
		if a.Min == nil || a.Max == nil || *a.Min > *a.Max {
			return errors.New("integer needs min and max, with min no greater than max")
		}
	case "boolean":
	case "list":
		if a.Items == nil || a.MaxItems <= 0 {
			return errors.New("list needs items and a positive max_items")
		}
		return validateArgTypeAndBounds(*a.Items)
	case "object":
		if len(a.Fields) == 0 {
			return errors.New("object needs fields")
		}
		return validateDeclaredArgs(a.Fields)
	default:
		return fmt.Errorf("unsupported type %q", a.Type)
	}
	return nil
}
