package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/henrico813/neurokit/internal/manifest"
	"github.com/henrico813/neurokit/internal/wire"
)

const (
	// Room beside the handler's answer for the write field, the request ID
	// and the broker's message.
	replyEnvelopeBytes = 4 << 10
	// A broker replies change-unknown up to 2 s after a handler's deadline,
	// once it has stopped the handler, so the client waits longer than that.
	deadlineMargin = 5 * time.Second
	// Broker health checks run every 5 s, so each one ends before the next
	// starts.
	healthTimeout  = 3 * time.Second
	localBrokerURL = "http://127.0.0.1:8080"
)

// Sends the body it checked, not the decoded arguments, because those no
// longer hold the approval label that the broker writes to its audit log.
func (p printer) checkAndSendCommand(
	m *manifest.Manifest,
	name string,
	cmd *manifest.Command,
	body []byte,
) int {
	if _, _, err := cmd.DecodeAndValidateArgs(body); err != nil {
		return p.refuseArguments(cmd, err)
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cmd.DeadlineMS)*time.Millisecond+deadlineMargin,
	)
	defer cancel()
	brokerURL := strings.TrimSuffix(os.Getenv(m.BrokerURLEnv), "/")
	if brokerURL == "" {
		err := fmt.Errorf("%s is not set", m.BrokerURLEnv)
		return p.printReply(describeMissingReply(cmd.Mutating, false, err))
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		brokerURL+wire.CommandRoutePrefix+name,
		bytes.NewReader(body),
	)
	if err != nil {
		return p.printReply(describeMissingReply(cmd.Mutating, false, err))
	}
	req.Header.Set(wire.SpecHeader, strconv.Itoa(m.Spec))
	// The broker's JSON writer turns each <, > or & into six characters.
	reply, connected, err := sendRequest(
		req,
		6*cmd.MaxResponseBytes+replyEnvelopeBytes,
	)
	switch {
	case err != nil:
		reply = describeMissingReply(cmd.Mutating, connected, err)
	case cmd.Mutating:
		reply = markUnclearReplyUnknown(reply)
	}
	return p.printReply(reply)
}

// Prints the reply from /readyz as Call does. An unset or empty URL variable
// means the check runs in the broker's container, so it asks serve's port.
func Health(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	p := printer{
		stdout:     stdout,
		stderr:     stderr,
		subcommand: "health",
	}
	if len(args) != 1 {
		return p.refuseArguments(
			nil,
			errors.New("health needs a manifest"),
		)
	}
	m, _, err := manifest.ReadAndValidate(args[0])
	if err != nil {
		return p.refuseArguments(nil, err)
	}
	brokerURL := strings.TrimSuffix(os.Getenv(m.BrokerURLEnv), "/")
	if brokerURL == "" {
		brokerURL = localBrokerURL
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		healthTimeout,
	)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		brokerURL+wire.ReadyzRoute,
		nil,
	)
	if err != nil {
		return p.printReply(describeMissingReply(false, false, err))
	}
	// /readyz replies only {"result":"ok"} or a short error, so 4 KiB is plenty.
	reply, _, err := sendRequest(req, replyEnvelopeBytes)
	if err != nil {
		reply = describeMissingReply(false, false, err)
	}
	return p.printReply(reply)
}

// Returns an error unless the reply is usable: one JSON reply within
// maxBytes, with a result and a 2xx status, or an error with a code and
// any other status. connected is true once the request may have reached
// the broker.
func sendRequest(
	req *http.Request,
	maxBytes int64,
) (
	reply wire.Reply,
	connected bool,
	err error,
) {
	var gotConn atomic.Bool
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { gotConn.Store(true) },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := newBrokerClient().Do(req)
	if err != nil {
		return reply, gotConn.Load(), err
	}
	defer resp.Body.Close()
	err = wire.DecodeOneJSONValue(resp.Body, maxBytes, &reply)
	isSuccess := resp.StatusCode >= 200 && resp.StatusCode < 300
	switch {
	case err != nil:
		err = fmt.Errorf(
			"HTTP %d with a body that is not a reply: %w",
			resp.StatusCode,
			err,
		)
	case isSuccess && (reply.Result == nil || reply.Error != nil),
		!isSuccess && (reply.Error == nil || reply.Error.Code == "" || reply.Result != nil):
		err = fmt.Errorf(
			"HTTP %d with a reply that does not match it",
			resp.StatusCode,
		)
	}
	return reply, true, err
}

// Brokers sit on internal networks, so a proxy or a redirect could only send
// a request, even a change, to the wrong place.
func newBrokerClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the broker replied with a redirect, which neurokit never follows")
		},
	}
}

// After connecting, the broker may already have run the handler, so a
// mutation is change-unknown, never not-sent, which callers read as safe
// to retry.
func describeMissingReply(
	mutating bool,
	connected bool,
	err error,
) wire.Reply {
	reply := wire.Reply{
		Error: &wire.ErrorDetail{
			Code:    wire.CodeBrokerUnavailable,
			Message: "no usable reply from the broker: " + err.Error(),
		},
	}
	switch {
	case mutating && connected:
		reply.Error.Code = wire.CodeChangeUnknown
		reply.Write = wire.WriteStateUnknown
	case mutating:
		reply.Write = wire.WriteStateNotSent
	}
	return reply
}

// Applies the broker's unclear-change rule again, so an old or broken broker
// cannot turn a forgotten write into a success.
func markUnclearReplyUnknown(reply wire.Reply) wire.Reply {
	var code, requestID string
	if reply.Error != nil {
		code, requestID = reply.Error.Code, reply.Error.RequestID
	}
	newCode, newWrite := wire.MarkUnclearChangeAsUnknown(code, reply.Write)
	if newCode == code && newWrite == reply.Write {
		return reply
	}
	return wire.Reply{
		Error: &wire.ErrorDetail{
			Code:      newCode,
			Message:   "the change may or may not have reached the provider; read its state before trying again",
			RequestID: requestID,
		},
		Write: newWrite,
	}
}
