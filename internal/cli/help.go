package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/henrico813/neurokit/internal/manifest"
)

// Prints plain text and returns 0, or refuses bad arguments as Call does.
// With no manifest it still prints the version, so a new install can show it.
func Help(
	args []string,
	version string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	p := printer{
		stdout:     stdout,
		stderr:     stderr,
		subcommand: "help",
	}
	var m *manifest.Manifest
	var err error
	switch len(args) {
	case 0:
	case 1:
		m, _, err = manifest.ReadAndValidate(args[0])
	case 2:
		m, _, err = loadCommand(args[0], args[1])
	default:
		err = errors.New("help takes at most a manifest and a command")
	}
	if err != nil {
		return p.refuseArguments(nil, err)
	}
	fmt.Fprintf(stdout, "neurokit %s, spec %d\n", version, manifest.Spec)
	switch len(args) {
	case 0:
		fmt.Fprintln(stdout, Usage)
	case 1:
		printCommandList(stdout, m)
	case 2:
		printCommandFlags(stdout, m.Commands[args[1]])
	}
	return 0
}

func printCommandList(w io.Writer, m *manifest.Manifest) {
	fmt.Fprintf(w, "%s commands:\n", m.Name)
	for _, name := range slices.Sorted(maps.Keys(m.Commands)) {
		if m.Commands[name].Mutating {
			name += " (mutating)"
		}
		fmt.Fprintln(w, "  "+name)
	}
	fmt.Fprintln(w, usages["run"])
}

func printCommandFlags(w io.Writer, cmd *manifest.Command) {
	for _, argName := range slices.Sorted(maps.Keys(cmd.Args)) {
		a := cmd.Args[argName]
		flagText := "--" + strings.ReplaceAll(argName, "_", "-")
		// A boolean flag alone means true, so false is written --name=false.
		if a.Type == "boolean" {
			flagText += "[=false]"
		}
		fmt.Fprintf(w, "  %s: %s\n", flagText, describeArg(a))
	}
	if cmd.Approval {
		fmt.Fprintln(w, "  --approval: a label for the broker's audit log, optional")
	}
	fmt.Fprintln(w, usages["run"])
}

func describeArg(a manifest.Arg) string {
	var text string
	switch {
	case isNestedArg(a):
		text = "nested values; use neurokit call"
	case a.Type == "string":
		text = fmt.Sprintf("text of at most %d characters", a.MaxLength)
	case a.Type == "integer":
		text = fmt.Sprintf("whole number from %d to %d", *a.Min, *a.Max)
	case a.Type == "boolean":
		text = "true or false"
	case a.Type == "list":
		text = fmt.Sprintf(
			"repeat for up to %d items, each %s",
			a.MaxItems,
			describeArg(*a.Items),
		)
	}
	if a.Optional {
		text += ", optional"
	}
	return text
}
