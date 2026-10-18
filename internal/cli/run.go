package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/henrico813/neurokit/internal/manifest"
)

// Turns flags, such as --slot-id for slot_id, into arguments and sends them as
// Call does. --help prints the command's help instead.
func Run(
	args []string,
	version string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	p := printer{
		stdout:     stdout,
		stderr:     stderr,
		subcommand: "run",
	}
	if len(args) < 2 {
		return p.refuseArguments(
			nil,
			errors.New("run needs a manifest and a command"),
		)
	}
	m, cmd, err := loadCommand(args[0], args[1])
	if err != nil {
		return p.refuseArguments(nil, err)
	}
	body, err := buildBodyFromFlags(args[1], cmd, args[2:])
	if errors.Is(err, flag.ErrHelp) {
		return Help(args[:2], version, stdout, stderr)
	}
	if err != nil {
		return p.refuseArguments(cmd, err)
	}
	return p.checkAndSendCommand(m, args[1], cmd, body)
}

// Leaves out each argument whose flag is not given, so the manifest check
// refuses a missing one and an optional one stays out of the request.
func buildBodyFromFlags(
	name string,
	cmd *manifest.Command,
	flags []string,
) ([]byte, error) {
	values := map[string]any{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, argName := range slices.Sorted(maps.Keys(cmd.Args)) {
		a := cmd.Args[argName]
		setValue := func(text string) error {
			if isNestedArg(a) {
				return errors.New("flags cannot hold nested values; use neurokit call with one JSON object")
			}
			itemType := a.Type
			if a.Type == "list" {
				itemType = a.Items.Type
			}
			// Values that are not text go to the manifest check as JSON, so
			// run and call refuse a bad number or boolean in the same words.
			value := any(text)
			if itemType != "string" {
				if !json.Valid([]byte(text)) {
					return errors.New("must be a number, true or false")
				}
				value = json.RawMessage(text)
			}
			if a.Type == "list" {
				list, _ := values[argName].([]any)
				value = append(list, value)
			}
			values[argName] = value
			return nil
		}
		flagName := strings.ReplaceAll(argName, "_", "-")
		if a.Type == "boolean" {
			fs.BoolFunc(flagName, "", setValue)
		} else {
			fs.Func(flagName, "", setValue)
		}
	}
	if cmd.Approval {
		fs.Func(
			"approval",
			"",
			func(label string) error {
				values["approval"] = label
				return nil
			},
		)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf(
			"unexpected argument %q; every argument is a flag",
			fs.Arg(0),
		)
	}
	return json.Marshal(values)
}

// Flags cannot write nested values clearly, so run refuses them and points
// to call.
func isNestedArg(a manifest.Arg) bool {
	return a.Type == "object" || a.Type == "list" && (a.Items.Type == "object" || a.Items.Type == "list")
}
