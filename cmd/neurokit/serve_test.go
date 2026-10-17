package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henrico813/neurokit/internal/runner"
	"github.com/henrico813/neurokit/internal/wire"
)

const testManifest = `{
  "name": "demo", "spec": 1, "broker_url_env": "DEMO_BROKER_URL", "max_handlers": 1,
  "program": ["sh", "DIR/handler.sh"], "check": ["true"],
  "commands": {
    "lookup": {"max_body_bytes": 64, "max_response_bytes": 100, "deadline_ms": 5000,
      "args": {"query": {"type": "string", "max_length": 20}}},
    "swap": {"mutating": true, "lock": "write", "max_body_bytes": 64, "max_response_bytes": 100, "deadline_ms": 5000,
      "args": {"position": {"type": "integer", "min": 1, "max": 9}}}
  }
}`

// Writes the manifest and handler.sh into a new folder, with DIR in either
// replaced by that folder.
func writeTool(
	t *testing.T,
	manifestText,
	handlerScript string,
) (
	manifestPath,
	dir string,
) {
	t.Helper()
	dir = t.TempDir()
	manifestPath = filepath.Join(dir, "manifest.json")
	for path, text := range map[string]string{
		manifestPath:                     manifestText,
		filepath.Join(dir, "handler.sh"): handlerScript,
	} {
		if err := os.WriteFile(
			path,
			[]byte(strings.ReplaceAll(text, "DIR", dir)),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	return manifestPath, dir
}

// Starts serve in this process on a free local port. stopServe does what
// SIGTERM does and returns serve's error; read log only after it returns.
func startServe(
	t *testing.T,
	manifestPath string,
) (
	url string,
	stopServe func() error,
	log *bytes.Buffer,
) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	stop, sendSIGTERM := context.WithCancel(context.Background())
	log = new(bytes.Buffer)
	served := make(chan error, 1)
	go func() {
		listen := func() (net.Listener, error) { return ln, nil }
		served <- serveBroker(
			stop,
			manifestPath,
			listen,
			slog.New(slog.NewJSONHandler(log, nil)),
		)
	}()
	stopServe = sync.OnceValue(func() error {
		// Go's client sometimes opens a spare connection that sends nothing,
		// and serve's stop would wait seconds for it.
		http.DefaultClient.CloseIdleConnections()
		sendSIGTERM()
		return <-served
	})
	t.Cleanup(func() { stopServe() })
	return "http://" + ln.Addr().String(), stopServe, log
}

type reply struct {
	status int
	wire.Reply
}

func postCommand(t *testing.T, url, command string, body io.Reader) reply {
	t.Helper()
	// A success reply has no error, so this empty one lets tests read Error.Code
	// on any reply.
	got := reply{Reply: wire.Reply{Error: &wire.ErrorDetail{}}}
	r, err := http.NewRequest(http.MethodPost, url+"/commands/"+command, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Neurokit-Spec", "1")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Errorf("POST %s: %v", command, err)
		return got
	}
	defer resp.Body.Close()
	got.status = resp.StatusCode
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Errorf("POST %s: reply is not JSON: %v", command, err)
	}
	return got
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for range 100 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// A broker missing a secret must not serve, and must say why in one line
// instead of restarting over and over with no reason shown.
func TestServeStopsWhenCheckFails(t *testing.T) {
	failingCheck := strings.Replace(
		testManifest,
		`"check": ["true"]`,
		`"check": ["sh", "-c", "echo starting; echo 'GEMINI_API_KEY is required' >&2; exit 1"]`,
		1,
	)
	manifestPath, _ := writeTool(t, failingCheck, "")
	listen := func() (net.Listener, error) {
		return nil, errors.New("serve listened")
	}

	err := serveBroker(
		context.Background(),
		manifestPath,
		listen,
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)

	if err == nil || err.Error() != "check failed: GEMINI_API_KEY is required" {
		t.Errorf(
			"serve returned %v; want \"check failed: GEMINI_API_KEY is required\", before listening",
			err,
		)
	}
}

// Compose marks the broker healthy only while /readyz passes, so it must fail
// while a secret is missing. Most tools have no ready program, and then it
// must pass.
func TestReadyzRunsReadyProgram(t *testing.T) {
	withReady := strings.Replace(
		testManifest,
		`"check": ["true"]`,
		`"check": ["true"], "ready": ["test", "-r", "DIR/secret"]`,
		1,
	)
	readyPath, dir := writeTool(t, withReady, "")
	noReadyPath, _ := writeTool(t, testManifest, "")
	readyURL, _, _ := startServe(t, readyPath)
	noReadyURL, _, _ := startServe(t, noReadyPath)

	readyStatus := func(url string) int {
		resp, err := http.Get(url + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	withoutSecret := readyStatus(readyURL)
	if err := os.WriteFile(
		filepath.Join(dir, "secret"),
		nil,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	withSecret := readyStatus(readyURL)
	withoutReady := readyStatus(noReadyURL)

	if withoutSecret != 503 || withSecret != 200 || withoutReady != 200 {
		t.Errorf(
			"/readyz = %d without the secret, %d with it, %d with no ready program; want 503, 200, 200",
			withoutSecret,
			withSecret,
			withoutReady,
		)
	}
}

// On stop, a running change must report that it may have happened, a waiting
// one must never run, and serve must exit soon, but only after both replies.
func TestSigtermStopsHandlersAndQueue(t *testing.T) {
	// The handler ignores SIGTERM, so it is killed only at the end of the grace
	// period, and serve must still wait for its reply.
	manifestPath, dir := writeTool(
		t,
		testManifest,
		`trap '' TERM; touch DIR/started; sleep 30`,
	)
	url, stopServe, log := startServe(t, manifestPath)
	running, queued := make(chan reply), make(chan reply)
	go func() {
		running <- postCommand(
			t,
			url,
			"swap",
			strings.NewReader(`{"position":3}`),
		)
	}()
	waitForFile(t, filepath.Join(dir, "started"))
	// A swap runs alone, so the second one waits for the first.
	go func() {
		queued <- postCommand(
			t,
			url,
			"swap",
			strings.NewReader(`{"position":4}`),
		)
	}()
	time.Sleep(100 * time.Millisecond) // so serve accepts the waiting request's connection before it stops listening

	start := time.Now()
	err := stopServe()
	elapsed := time.Since(start)
	repliesWritten := strings.Count(log.String(), `"path":"/commands/swap"`)

	if got := <-running; got.status != 502 || got.Error.Code != wire.CodeChangeUnknown || got.Write != wire.WriteStateUnknown {
		t.Errorf(
			"running swap = %d %+v, write %q; want 502 change-unknown, write unknown",
			got.status,
			got.Error,
			got.Write,
		)
	}
	if got := <-queued; got.status != 503 || got.Error.Code != wire.CodeNotReady || got.Write != wire.WriteStateNotSent {
		t.Errorf(
			"queued swap = %d %+v, write %q; want 503 not-ready, write not-sent",
			got.status,
			got.Error,
			got.Write,
		)
	}
	if err != nil || elapsed >= runner.GracePeriod+time.Second || repliesWritten != 2 {
		t.Errorf(
			"serve returned %v after %v, with %d replies written; want nil within the grace period plus 1 s, after both",
			err,
			elapsed,
			repliesWritten,
		)
	}
}

// Go's write timeout starts before the body arrives. If it is too short, a
// slow client loses the reply to a change that was made, and must then report
// that it does not know whether the change happened.
func TestSlowBodyStillGetsReply(t *testing.T) {
	shortDeadlines := strings.ReplaceAll(
		testManifest,
		`"deadline_ms": 5000`,
		`"deadline_ms": 500`,
	)
	manifestPath, _ := writeTool(
		t,
		shortDeadlines,
		`echo '{"result":"swapped","write":"verified"}' >&3`,
	)
	url, _, _ := startServe(t, manifestPath)
	body, sendBody := io.Pipe()
	go func() {
		for _, part := range []string{`{"posi`, `tion":`, `3}`} {
			sendBody.Write([]byte(part))
			time.Sleep(1500 * time.Millisecond)
		}
		sendBody.Close()
	}()

	got := postCommand(t, url, "swap", body)

	if got.status != 200 || got.Write != wire.WriteStateVerified {
		t.Errorf(
			"swap = %d %+v, write %q; want 200, write verified",
			got.status,
			got.Error,
			got.Write,
		)
	}
}

// One search for a request ID must find every line its handler logged.
// Handlers learn the command, whether it may change anything and how long
// they have only from the NEUROKIT_ variables.
func TestRequestIDReachesHandlerAndLogs(t *testing.T) {
	manifestPath, _ := writeTool(
		t,
		testManifest,
		`echo "$NEUROKIT_COMMAND $NEUROKIT_REQUEST_ID $NEUROKIT_MUTATING $NEUROKIT_DEADLINE_MS"
		echo '{"error":{"code":"provider-unavailable","message":"down"}}' >&3
		exit 1`,
	)
	url, stopServe, log := startServe(t, manifestPath)

	got := postCommand(t, url, "lookup", strings.NewReader(`{"query":"kim"}`))
	stopServe()

	var entry struct {
		RequestID string `json:"request_id"`
		Command   string `json:"command"`
		Line      string `json:"line"`
	}
	for line := range strings.Lines(log.String()) {
		if strings.Contains(line, `"msg":"handler_output"`) {
			json.Unmarshal([]byte(line), &entry)
		}
	}
	var command, requestID, mutating string
	var deadlineMS int
	fmt.Sscan(entry.Line, &command, &requestID, &mutating, &deadlineMS)
	if got.Error.Code != wire.CodeProviderUnavailable || entry.RequestID != got.Error.RequestID || entry.Command != "lookup" {
		t.Errorf(
			"reply %+v, handler_output entry %+v; want provider-unavailable, and the entry naming the reply's request ID and lookup",
			got.Error,
			entry,
		)
	}
	if command != "lookup" || requestID != got.Error.RequestID || mutating != "0" || deadlineMS <= 4000 || deadlineMS > 5000 {
		t.Errorf(
			"handler saw NEUROKIT_COMMAND %q, NEUROKIT_REQUEST_ID %q, NEUROKIT_MUTATING %q, NEUROKIT_DEADLINE_MS %d; want lookup, the reply's request ID, 0, and nearly 5000",
			command,
			requestID,
			mutating,
			deadlineMS,
		)
	}
}
