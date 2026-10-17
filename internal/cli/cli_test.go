package cli_test

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/henrico813/neurokit/internal/cli"
	"github.com/henrico813/neurokit/internal/wire"
)

const testManifest = `{
  "name": "demo", "spec": 1, "broker_url_env": "DEMO_BROKER_URL", "max_handlers": 1,
  "program": ["demo"], "check": ["true"],
  "commands": {
    "lookup": {"max_body_bytes": 200, "max_response_bytes": 100, "deadline_ms": 100,
      "args": {"query": {"type": "string", "max_length": 20}}},
    "swap": {"mutating": true, "approval": true, "max_body_bytes": 200, "max_response_bytes": 4096, "deadline_ms": 100,
      "args": {"position": {"type": "integer", "min": 1, "max": 9}}}
  }
}`

var validArgs = map[string]string{
	"lookup": `{"query":"kim"}`,
	"swap":   `{"position":3}`,
}

type output struct {
	exit   int
	stdout string
	stderr string
	wire.Reply
}

// Starts a fake broker, points DEMO_BROKER_URL at it, and returns the path
// of a manifest for it. The URL ends in a slash, as an operator might write
// it.
func startBroker(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("DEMO_BROKER_URL", srv.URL+"/")
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCall(t *testing.T, args ...string) output {
	t.Helper()
	var stdout, stderr bytes.Buffer
	got := output{exit: cli.Call(args, &stdout, &stderr)}
	got.stdout, got.stderr = stdout.String(), stderr.String()
	// A success reply has no error, so this empty one lets tests read
	// Error.Code on any reply.
	got.Reply = wire.Reply{Error: &wire.ErrorDetail{}}
	if err := wire.DecodeOneJSONValue(&stdout, 1<<20, &got.Reply); err != nil {
		t.Errorf("stdout %q is not one reply: %v", got.stdout, err)
	}
	return got
}

// Without the spec header the broker refuses every call, and a body rebuilt
// from the checked arguments would lose the approval label the audit needs.
func TestSendsCheckedBodyWithSpecHeader(t *testing.T) {
	var gotPath, gotSpec, gotBody string
	manifestPath := startBroker(
		t,
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotPath = r.URL.Path
			gotSpec = r.Header.Get("Neurokit-Spec")
			gotBody = string(body)
			io.WriteString(w, `{"result":"swapped","write":"verified"}`)
		},
	)
	const body = `{"position":3,"approval":"policy:test"}`

	got := runCall(t, manifestPath, "swap", body)

	if got.exit != 0 || gotPath != "/commands/swap" || gotSpec != "1" || gotBody != body {
		t.Errorf(
			"exit %d; broker got %s with Neurokit-Spec %q and body %s; want exit 0, /commands/swap, spec 1 and %s",
			got.exit,
			gotPath,
			gotSpec,
			gotBody,
			body,
		)
	}
}

// Exit 2 must mean the same as the broker's invalid-request: nothing ran, so
// the caller fixes the arguments instead of checking whether a change happened.
func TestBadArgumentsExitTwoWithoutSending(t *testing.T) {
	manifestPath := startBroker(
		t,
		func(http.ResponseWriter, *http.Request) {
			t.Error("the broker got a request")
		},
	)

	got := runCall(t, manifestPath, "swap", `{"position":3,"colour":"red"}`)

	if got.exit != 2 || got.Error.Code != wire.CodeInvalidRequest || got.Write != wire.WriteStateNotSent || !strings.Contains(got.stderr, "usage: neurokit call") {
		t.Errorf(
			"exit %d, %+v, write %q, stderr %q; want exit 2, invalid-request, write not-sent and a usage line",
			got.exit,
			got.Error,
			got.Write,
			got.stderr,
		)
	}
}

// Callers act on the exit status and body, so a mutation reply with no write,
// from an old or broken broker, must reach them as change-unknown, not as a
// success.
func TestReplyDecidesExitAndPrintedBody(t *testing.T) {
	// A handler answer of 4,000 & characters fits swap's 4096-byte cap, and
	// the broker sends each one as \u0026, six bytes.
	escapedAmpersands := strings.Repeat(`\u0026`, 4000)
	tests := []struct {
		name          string
		command       string
		status        int
		brokerBody    string
		wantExit      int
		wantResult    string
		wantCode      string
		wantWrite     wire.WriteState
		wantRequestID string
		wantStderr    string
	}{
		{
			name:       "list result",
			command:    "lookup",
			status:     200,
			brokerBody: `{"result":[1,2]}`,
			wantExit:   0,
			wantResult: `[1,2]`,
		},
		{
			name:          "read error",
			command:       "lookup",
			status:        502,
			brokerBody:    `{"error":{"code":"provider-unavailable","message":"down","request_id":"req-1"}}`,
			wantExit:      1,
			wantCode:      wire.CodeProviderUnavailable,
			wantRequestID: "req-1",
			wantStderr:    "neurokit call: provider-unavailable: down (req-1)\n",
		},
		{
			name:       "verified mutation",
			command:    "swap",
			status:     200,
			brokerBody: `{"result":"swapped","write":"verified"}`,
			wantExit:   0,
			wantResult: `"swapped"`,
			wantWrite:  wire.WriteStateVerified,
		},
		{
			name:       "escaped mutation result near the cap",
			command:    "swap",
			status:     200,
			brokerBody: `{"result":"` + escapedAmpersands + `","write":"verified"}`,
			wantExit:   0,
			wantResult: `"` + escapedAmpersands + `"`,
			wantWrite:  wire.WriteStateVerified,
		},
		{
			name:       "mutation result without write",
			command:    "swap",
			status:     200,
			brokerBody: `{"result":"swapped"}`,
			wantExit:   1,
			wantCode:   wire.CodeChangeUnknown,
			wantWrite:  wire.WriteStateUnknown,
			wantStderr: "neurokit call: change-unknown: ",
		},
		{
			name:          "mutation error without write",
			command:       "swap",
			status:        502,
			brokerBody:    `{"error":{"code":"provider-rejected","message":"no","request_id":"req-2"}}`,
			wantExit:      1,
			wantCode:      wire.CodeChangeUnknown,
			wantWrite:     wire.WriteStateUnknown,
			wantRequestID: "req-2",
			wantStderr:    "neurokit call: change-unknown: ",
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				manifestPath := startBroker(
					t,
					func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(tt.status)
						io.WriteString(w, tt.brokerBody)
					},
				)

				got := runCall(t, manifestPath, tt.command, validArgs[tt.command])

				if got.exit != tt.wantExit || string(got.Result) != tt.wantResult || got.Error.Code != tt.wantCode || got.Write != tt.wantWrite || got.Error.RequestID != tt.wantRequestID || !strings.HasPrefix(got.stderr, tt.wantStderr) {
					t.Errorf(
						"exit %d, stdout %s, stderr %q; want exit %d, result %s, code %q, write %q, request ID %q, stderr starting %q",
						got.exit,
						got.stdout,
						got.stderr,
						tt.wantExit,
						tt.wantResult,
						tt.wantCode,
						tt.wantWrite,
						tt.wantRequestID,
						tt.wantStderr,
					)
				}
			},
		)
	}
}

// A tool's old client reported a change cut off after sending as never sent,
// so it could be repeated. Only a refused connection proves nothing was sent.
func TestNoReplyOutcomeDependsOnConnect(t *testing.T) {
	tests := []struct {
		name      string
		broker    http.HandlerFunc
		wantCode  string
		wantWrite wire.WriteState
	}{
		{
			name:      "refused connection",
			broker:    nil,
			wantCode:  wire.CodeBrokerUnavailable,
			wantWrite: wire.WriteStateNotSent,
		},
		{
			name: "reset after send",
			broker: func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
			},
			wantCode:  wire.CodeChangeUnknown,
			wantWrite: wire.WriteStateUnknown,
		},
		{
			name: "HTML from a proxy",
			broker: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, "<html>Bad Gateway</html>")
			},
			wantCode:  wire.CodeChangeUnknown,
			wantWrite: wire.WriteStateUnknown,
		},
		{
			name: "redirect",
			broker: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			},
			wantCode:  wire.CodeChangeUnknown,
			wantWrite: wire.WriteStateUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				manifestPath := startBroker(
					t,
					func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/commands/swap" {
							t.Errorf(
								"the broker got a request for %s; want only /commands/swap",
								r.URL.Path,
							)
							return
						}
						tt.broker(w, r)
					},
				)
				// Nothing listens on a port that was just closed.
				if tt.broker == nil {
					ln, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					ln.Close()
					t.Setenv("DEMO_BROKER_URL", "http://"+ln.Addr().String())
				}

				got := runCall(t, manifestPath, "swap", validArgs["swap"])

				if got.exit != 1 || got.Error.Code != tt.wantCode || got.Write != tt.wantWrite {
					t.Errorf(
						"exit %d, %+v, write %q; want exit 1, %s, write %q",
						got.exit,
						got.Error,
						got.Write,
						tt.wantCode,
						tt.wantWrite,
					)
				}
			},
		)
	}
}

// A client that gives up at the deadline invents its own change-unknown
// before the broker's reply; one that never gives up hangs its caller.
func TestClientWaitsDeadlinePlusFiveSeconds(t *testing.T) {
	manifestPath := startBroker(
		t,
		func(_ http.ResponseWriter, r *http.Request) {
			io.ReadAll(r.Body)
			<-r.Context().Done()
		},
	)

	start := time.Now()
	got := runCall(t, manifestPath, "swap", validArgs["swap"])
	elapsed := time.Since(start)

	if elapsed < 5100*time.Millisecond || elapsed > 6*time.Second || got.Error.Code != wire.CodeChangeUnknown || got.Write != wire.WriteStateUnknown {
		t.Errorf(
			"gave up after %v with %+v, write %q; want change-unknown, write unknown, 5.1 s after sending",
			elapsed,
			got.Error,
			got.Write,
		)
	}
}
