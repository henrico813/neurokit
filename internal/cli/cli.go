// Package cli checks a command's arguments against the tool's manifest,
// sends them to the broker, and prints the reply, or the lack of one, as the
// README's "Calling a broker" section describes.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/henrico813/neurokit/internal/manifest"
	"github.com/henrico813/neurokit/internal/wire"
)

var usages = map[string]string{
	"call": "usage: neurokit call <manifest> <command> <json>",
}

type printer struct {
	stdout     io.Writer
	stderr     io.Writer
	subcommand string
}

// Prints one JSON object on stdout. Returns 0 for a result, 1 for an error,
// and 2 for bad arguments, which it never sends.
func Call(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	p := printer{
		stdout:     stdout,
		stderr:     stderr,
		subcommand: "call",
	}
	if len(args) != 3 {
		return p.refuseArguments(
			nil,
			errors.New("call needs a manifest, a command and one JSON object"),
		)
	}
	m, cmd, err := loadCommand(args[0], args[1])
	if err != nil {
		return p.refuseArguments(nil, err)
	}
	return p.checkAndSendCommand(m, args[1], cmd, []byte(args[2]))
}

func loadCommand(
	manifestPath string,
	name string,
) (
	*manifest.Manifest,
	*manifest.Command,
	error,
) {
	m, _, err := manifest.ReadAndValidate(manifestPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := m.Commands[name]
	if cmd == nil {
		return nil, nil, fmt.Errorf("%s has no command %q", m.Name, name)
	}
	return m, cmd, nil
}

// The stderr line ends with the request ID, when there is one, so a person
// can find the broker's log entry.
func (p printer) printReply(reply wire.Reply) int {
	json.NewEncoder(p.stdout).Encode(reply)
	if reply.Error == nil {
		return 0
	}
	line := fmt.Sprintf(
		"neurokit %s: %s: %s",
		p.subcommand,
		reply.Error.Code,
		reply.Error.Message,
	)
	if reply.Error.RequestID != "" {
		line += " (" + reply.Error.RequestID + ")"
	}
	fmt.Fprintln(p.stderr, line)
	return 1
}

// Nothing was sent, so a refused mutation says not-sent.
func (p printer) refuseArguments(cmd *manifest.Command, err error) int {
	reply := wire.Reply{
		Error: &wire.ErrorDetail{
			Code:    wire.CodeInvalidRequest,
			Message: err.Error(),
		},
	}
	if cmd != nil && cmd.Mutating {
		reply.Write = wire.WriteStateNotSent
	}
	p.printReply(reply)
	fmt.Fprintln(p.stderr, usages[p.subcommand])
	return 2
}
