package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/henrico813/neurokit/internal/manifest"
	"github.com/henrico813/neurokit/internal/wire"
)

type callerReply struct {
	result  json.RawMessage
	code    string
	message string
	write   wire.WriteState
	detail  string
}

// No handler has run when a request is refused, so a mutation's refusal says
// not-sent.
func refuseBeforeRun(cmd *manifest.Command, code, message string) *callerReply {
	reply := &callerReply{
		code:    code,
		message: message,
	}
	if cmd.Mutating {
		reply.write = wire.WriteStateNotSent
	}
	return reply
}

// Only exit 0 with a result, or exit 1 with an error, is a real answer;
// anything else is a crash or a timeout. A mutation whose result is unclear
// replies change-unknown, never success or not-sent.
func buildReplyFromHandlerRun(
	cmd *manifest.Command,
	handlerRun RunResult,
	timedOut bool,
) callerReply {
	var answer wire.Reply
	err := wire.DecodeOneJSONValue(
		bytes.NewReader(handlerRun.Answer),
		cmd.MaxResponseBytes,
		&answer,
	)
	var reply callerReply
	switch {
	case err == nil && handlerRun.ExitCode == 0 && answer.Result != nil && answer.Error == nil:
		reply = callerReply{
			result: answer.Result,
			write:  answer.Write,
		}
	case err == nil && handlerRun.ExitCode == 1 && answer.Error != nil && answer.Result == nil:
		reply = callerReply{
			code:    answer.Error.Code,
			message: answer.Error.Message,
			write:   answer.Write,
		}
		if !isAllowedHandlerCode(cmd, reply.code) {
			reply.code, reply.message = wire.CodeUnexpected, "the handler failed; the broker log has details"
			reply.detail = fmt.Sprintf(
				"handler sent undeclared code %q",
				answer.Error.Code,
			)
		}
	case timedOut:
		reply = callerReply{
			code:    wire.CodeDeadlineExceeded,
			message: "the command ran out of time",
		}
	default:
		reply = callerReply{
			code:    wire.CodeUnexpected,
			message: "the handler failed; the broker log has details",
			detail: fmt.Sprintf(
				"handler exited %d; reading its answer: %v",
				handlerRun.ExitCode,
				err,
			),
		}
	}
	if !cmd.Mutating {
		reply.write = ""
		return reply
	}
	if code, write := wire.MarkUnclearChangeAsUnknown(
		reply.code,
		reply.write,
	); code != reply.code || write != reply.write {
		reply = callerReply{
			code:    code,
			message: "the change may or may not have reached the provider; read its state before trying again",
			write:   write,
			detail: strings.TrimSpace(fmt.Sprintf(
				"was code %q with write %q. %s",
				reply.code,
				reply.write,
				reply.detail,
			)),
		}
	}
	return reply
}

// Shared codes a handler may send. The rest, such as spec-mismatch, report
// what only the broker or the client can know.
var handlerSharedCodes = []string{
	wire.CodeInvalidRequest,
	wire.CodeForbidden,
	wire.CodeNotFound,
	wire.CodeUnexpected,
	wire.CodeProviderUnavailable,
	wire.CodeProviderRejected,
	wire.CodeChangeUnknown,
	wire.CodeVerificationFailed,
}

func isAllowedHandlerCode(cmd *manifest.Command, code string) bool {
	return slices.Contains(handlerSharedCodes, code) || slices.Contains(cmd.Errors, code)
}

func writeReply(w http.ResponseWriter, requestID string, reply callerReply) {
	status := http.StatusOK
	body := wire.Reply{
		Result: reply.result,
		Write:  reply.write,
	}
	if reply.code != "" {
		status = wire.LookupHTTPStatus(reply.code)
		body = wire.Reply{
			Error: &wire.ErrorDetail{
				Code:      reply.code,
				Message:   reply.message,
				RequestID: requestID,
			},
			Write: reply.write,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
