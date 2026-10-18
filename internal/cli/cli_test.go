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
      "args": {"position": {"type": "integer", "min": 1, "max": 9},
        "slot_ids": {"type": "list", "items": {"type": "integer", "min": 1, "max": 9}, "max_items": 3, "optional": true},
        "keep_bench": {"type": "boolean", "optional": true},
        "filter": {"type": "object", "fields": {"team": {"type": "string", "max_length": 9}}, "optional": true},
        "filters": {"type": "list", "items": {"type": "object", "fields": {"team": {"type": "string", "max_length": 9}}}, "max_items": 2, "optional": true}}}
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

func runNeurokit(
	t *testing.T,
	subcommand string,
	args ...string,
) output {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var got output
	switch subcommand {
	case "run":
		got.exit = cli.Run(args, "v0.0.0-test", &stdout, &stderr)
	case "call":
		got.exit = cli.Call(args, &stdout, &stderr)
	case "health":
		got.exit = cli.Health(args, &stdout, &stderr)
	}
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

	got := runNeurokit(t, "call", manifestPath, "swap", body)

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

	tests := []struct {
		name        string
		subcommand  string
		swapArgs    []string
		wantMessage string
	}{
		{
			name:        "call with an unknown argument",
			subcommand:  "call",
			swapArgs:    []string{`{"position":3,"colour":"red"}`},
			wantMessage: "colour",
		},
		{
			name:        "run with an unknown flag",
			subcommand:  "run",
			swapArgs:    []string{"--position=3", "--colour=red"},
			wantMessage: "colour",
		},
		{
			name:        "run with an object argument",
			subcommand:  "run",
			swapArgs:    []string{"--position=3", "--filter=team"},
			wantMessage: "neurokit call",
		},
		{
			name:        "run with a list of objects",
			subcommand:  "run",
			swapArgs:    []string{"--position=3", `--filters={"team":"red"}`},
			wantMessage: "neurokit call",
		},
		{
			name:        "run with a word after a boolean",
			subcommand:  "run",
			swapArgs:    []string{"--position=3", "--keep-bench", "false"},
			wantMessage: "unexpected argument",
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				got := runNeurokit(
					t,
					tt.subcommand,
					append([]string{manifestPath, "swap"}, tt.swapArgs...)...,
				)

				wantUsage := "usage: neurokit " + tt.subcommand
				if got.exit != 2 || got.Error.Code != wire.CodeInvalidRequest || got.Write != wire.WriteStateNotSent || !strings.Contains(got.Error.Message, tt.wantMessage) || !strings.Contains(got.stderr, wantUsage) {
					t.Errorf(
						"exit %d, %+v, write %q, stderr %q; want exit 2, invalid-request naming %q, write not-sent and %q",
						got.exit,
						got.Error,
						got.Write,
						got.stderr,
						tt.wantMessage,
						wantUsage,
					)
				}
			},
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

				got := runNeurokit(
					t,
					"call",
					manifestPath,
					tt.command,
					validArgs[tt.command],
				)

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

				got := runNeurokit(t, "call", manifestPath, "swap", validArgs["swap"])

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
	got := runNeurokit(t, "call", manifestPath, "swap", validArgs["swap"])
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

// Every tool gets its flags from its manifest by one rule, so a skill can
// teach run without each tool inventing its own flags.
func TestRunSendsFlagsAsArguments(t *testing.T) {
	var gotBody string
	manifestPath := startBroker(
		t,
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			io.WriteString(w, `{"result":"swapped","write":"verified"}`)
		},
	)

	got := runNeurokit(
		t,
		"run",
		manifestPath,
		"swap",
		"--position=3",
		"--slot-ids=4",
		"--slot-ids=5",
		"--keep-bench",
		"--approval=policy:test",
	)

	const want = `{"approval":"policy:test","keep_bench":true,"position":3,"slot_ids":[4,5]}`
	if got.exit != 0 || gotBody != want {
		t.Errorf(
			"exit %d, stderr %q, broker got %s; want exit 0 and %s",
			got.exit,
			got.stderr,
			gotBody,
			want,
		)
	}
}

// A broker image's Compose health check runs where the URL variable is unset,
// so health must find serve on its own port.
func TestHealthUsesLocalBrokerWithoutEnv(t *testing.T) {
	// In CI a skip would leave the fallback untested, so a taken port fails.
	ln, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil && os.Getenv("CI") != "" {
		t.Fatalf("port 8080 is taken: %v", err)
	}
	if err != nil {
		t.Skipf("port 8080 is taken: %v", err)
	}
	var askedReadyz bool
	local := httptest.NewUnstartedServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/readyz" {
					askedReadyz = true
				}
				io.WriteString(w, `{"result":"ok"}`)
			},
		),
	)
	local.Listener.Close()
	local.Listener = ln
	local.Start()
	t.Cleanup(local.Close)
	manifestPath := startBroker(
		t,
		func(http.ResponseWriter, *http.Request) {},
	)
	for _, unset := range []bool{true, false} {
		t.Setenv("DEMO_BROKER_URL", "")
		if unset {
			os.Unsetenv("DEMO_BROKER_URL")
		}
		askedReadyz = false

		got := runNeurokit(t, "health", manifestPath)

		if got.exit != 0 || !askedReadyz {
			t.Errorf(
				"with DEMO_BROKER_URL unset %t: exit %d, stderr %q, local /readyz asked %t; want exit 0 from it",
				unset,
				got.exit,
				got.stderr,
				askedReadyz,
			)
		}
	}
}

// Compose trusts health's exit status, so a broker that is not ready, or
// never answers, must fail before the next check starts.
func TestHealthExitsOneUnlessBrokerReady(t *testing.T) {
	tests := []struct {
		name            string
		broker          http.HandlerFunc
		wantExit        int
		wantCode        string
		wantStderrLines int
	}{
		{
			name: "ready",
			broker: func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, `{"result":"ok"}`)
			},
			wantExit:        0,
			wantCode:        "",
			wantStderrLines: 0,
		},
		{
			name: "not ready",
			broker: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, `{"error":{"code":"not-ready","message":"ready failed","request_id":"req-3"}}`)
			},
			wantExit:        1,
			wantCode:        wire.CodeNotReady,
			wantStderrLines: 1,
		},
		{
			name: "never answers",
			broker: func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
			wantExit:        1,
			wantCode:        wire.CodeBrokerUnavailable,
			wantStderrLines: 1,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				manifestPath := startBroker(
					t,
					func(w http.ResponseWriter, r *http.Request) {
						// Other programs on a developer's machine may probe a new
						// port with GET /, so only /readyz gets the reply under test.
						if r.URL.Path != "/readyz" {
							http.NotFound(w, r)
							return
						}
						tt.broker(w, r)
					},
				)

				start := time.Now()
				got := runNeurokit(t, "health", manifestPath)
				elapsed := time.Since(start)

				if got.exit != tt.wantExit || got.Error.Code != tt.wantCode || strings.Count(got.stderr, "\n") != tt.wantStderrLines || elapsed > 4*time.Second {
					t.Errorf(
						"exit %d after %v, stdout %s, stderr %q; want exit %d, code %q and %d stderr lines within 4 s",
						got.exit,
						elapsed,
						got.stdout,
						got.stderr,
						tt.wantExit,
						tt.wantCode,
						tt.wantStderrLines,
					)
				}
			},
		)
	}
}
