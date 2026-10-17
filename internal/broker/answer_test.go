package broker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/henrico813/neurokit/internal/broker"
	"github.com/henrico813/neurokit/internal/wire"
)

// A tool's old client reported changes that might have happened as never
// sent, so callers could repeat them. Any unclear mutation result must reply
// change-unknown.
func TestHandlerAnswerDecidesReply(t *testing.T) {
	tests := []struct {
		name            string
		command         string
		exitCode        int
		answer          string
		waitForDeadline bool
		wantStatus      int
		wantCode        string
		wantWrite       wire.WriteState
		wantResult      string
	}{
		{name: "read result", command: "lookup", answer: `{"result":[1]}`, wantStatus: 200, wantResult: `[1]`},
		{name: "read shared code", command: "lookup", exitCode: 1, answer: `{"error":{"code":"provider-unavailable","message":"x"}}`, wantStatus: 502, wantCode: wire.CodeProviderUnavailable},
		{name: "read code declared elsewhere", command: "lookup", exitCode: 1, answer: `{"error":{"code":"lineup-locked","message":"x"}}`, wantStatus: 500, wantCode: wire.CodeUnexpected},
		{name: "read client-only code", command: "lookup", exitCode: 1, answer: `{"error":{"code":"broker-unavailable","message":"x"}}`, wantStatus: 500, wantCode: wire.CodeUnexpected},
		{name: "read exit 2", command: "lookup", exitCode: 2, answer: `{"result":[1]}`, wantStatus: 500, wantCode: wire.CodeUnexpected},
		{name: "read answer over max_response_bytes", command: "lookup", answer: `{"result":"` + strings.Repeat("a", 100) + `"}`, wantStatus: 500, wantCode: wire.CodeUnexpected},
		{name: "read past deadline", command: "lookup", waitForDeadline: true, wantStatus: 504, wantCode: wire.CodeDeadlineExceeded},
		{name: "mutation verified", command: "swap", answer: `{"result":1,"write":"verified"}`, wantStatus: 200, wantWrite: wire.WriteStateVerified, wantResult: `1`},
		{name: "mutation result without write", command: "swap", answer: `{"result":1}`, wantStatus: 502, wantCode: wire.CodeChangeUnknown, wantWrite: wire.WriteStateUnknown},
		{name: "mutation declared code not sent", command: "swap", exitCode: 1, answer: `{"error":{"code":"lineup-locked","message":"x"},"write":"not-sent"}`, wantStatus: 422, wantCode: "lineup-locked", wantWrite: wire.WriteStateNotSent},
		{name: "mutation error without write", command: "swap", exitCode: 1, answer: `{"error":{"code":"lineup-locked","message":"x"}}`, wantStatus: 502, wantCode: wire.CodeChangeUnknown, wantWrite: wire.WriteStateUnknown},
		{name: "mutation killed after answering", command: "swap", exitCode: -1, answer: `{"error":{"code":"lineup-locked","message":"x"},"write":"sent"}`, wantStatus: 502, wantCode: wire.CodeChangeUnknown, wantWrite: wire.WriteStateUnknown},
		{name: "mutation broker-only code", command: "swap", exitCode: 1, answer: `{"error":{"code":"deadline-exceeded","message":"x"},"write":"not-sent"}`, wantStatus: 500, wantCode: wire.CodeUnexpected, wantWrite: wire.WriteStateNotSent},
		{name: "mutation past deadline", command: "swap", waitForDeadline: true, wantStatus: 502, wantCode: wire.CodeChangeUnknown, wantWrite: wire.WriteStateUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newTestServer(t, shortDeadlines, func(ctx context.Context, _ broker.Job) broker.RunResult {
				if tt.waitForDeadline {
					<-ctx.Done()
					return broker.RunResult{ExitCode: -1}
				}
				return broker.RunResult{ExitCode: tt.exitCode, Answer: []byte(tt.answer)}
			})

			got := serveAndDecode(t, srv, newCommandRequest(tt.command, validBody[tt.command]))

			if got.status != tt.wantStatus || got.Error.Code != tt.wantCode || got.Write != tt.wantWrite || string(got.Result) != tt.wantResult {
				t.Errorf("got %d code %q write %q result %s; want %d code %q write %q result %s",
					got.status, got.Error.Code, got.Write, got.Result, tt.wantStatus, tt.wantCode, tt.wantWrite, tt.wantResult)
			}
		})
	}
}
