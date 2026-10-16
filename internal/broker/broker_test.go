package broker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henrico813/neurokit/internal/broker"
	"github.com/henrico813/neurokit/internal/manifest"
	"github.com/henrico813/neurokit/internal/wire"
)

const testManifest = `{
  "name": "demo", "spec": 1, "broker_url_env": "DEMO_BROKER_URL", "max_handlers": 2,
  "program": ["demo"], "check": ["demo", "--check"],
  "commands": {
    "lookup": {"max_body_bytes": 64, "max_response_bytes": 100, "deadline_ms": 5000,
      "args": {"query": {"type": "string", "max_length": 20}}},
    "swap": {"mutating": true, "lock": "write", "approval": true, "errors": ["lineup-locked"],
      "max_body_bytes": 300, "max_response_bytes": 100, "deadline_ms": 5000,
      "args": {"position": {"type": "integer", "min": 1, "max": 9}}}
  }
}`

var shortDeadlines = strings.ReplaceAll(testManifest, `"deadline_ms": 5000`, `"deadline_ms": 50`)

var validBody = map[string]string{"lookup": `{"query":"kim"}`, "swap": `{"position":3}`}

type runFunc func(ctx context.Context, job broker.Job) broker.RunResult

func (f runFunc) Run(ctx context.Context, job broker.Job) broker.RunResult {
	return f(ctx, job)
}

func failTestIfRun(t *testing.T) runFunc {
	return func(context.Context, broker.Job) broker.RunResult {
		t.Error("the runner was called")
		return broker.RunResult{}
	}
}

func newTestServer(t *testing.T, manifestText string, run runFunc) (*broker.Server, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(manifestText), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ReadAndValidate(path)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	isReady := func(context.Context) error { return nil }
	return broker.New(m, []byte(manifestText), run, isReady, slog.New(slog.NewJSONHandler(&log, nil))), &log
}

func newCommandRequest(command, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/commands/"+command, strings.NewReader(body))
	r.Header.Set("Neurokit-Spec", "1")
	return r
}

type reply struct {
	status int
	Result json.RawMessage  `json:"result"`
	Error  wire.ErrorDetail `json:"error"`
	Write  wire.WriteState  `json:"write"`
}

func findLogEntry(t *testing.T, log *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for line := range strings.Lines(log.String()) {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == msg {
			return entry
		}
	}
	t.Fatalf("no %q log entry in:\n%s", msg, log)
	return nil
}

func serveAndDecode(t *testing.T, srv http.Handler, r *http.Request) reply {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	got := reply{status: rec.Code}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s %s: body %q is not JSON: %v", r.Method, r.URL, rec.Body, err)
	}
	return got
}

// Handlers trust their input, so a bad request must be refused before any
// handler runs, and a refused mutation must say not-sent. Two older tool
// brokers had no body limit and read an empty body as {}.
func TestBadRequestsNeverReachRunner(t *testing.T) {
	srv, _ := newTestServer(t, testManifest, failTestIfRun(t))
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		wantCode   string
		wantWrite  wire.WriteState
	}{
		{name: "unknown route", method: "GET", target: "/status", wantStatus: 404, wantCode: wire.CodeNotFound},
		{name: "unknown command", method: "POST", target: "/commands/trade", body: `{}`, wantStatus: 404, wantCode: wire.CodeNotFound},
		{name: "GET on a command", method: "GET", target: "/commands/swap", wantStatus: 405, wantCode: wire.CodeMethodNotAllowed, wantWrite: wire.WriteStateNotSent},
		{name: "query string", method: "POST", target: "/commands/swap?position=3", body: `{"position":3}`, wantStatus: 400, wantCode: wire.CodeInvalidRequest, wantWrite: wire.WriteStateNotSent},
		{name: "valid JSON over max_body_bytes", method: "POST", target: "/commands/swap", body: `{"position":` + strings.Repeat(" ", 300) + `3}`, wantStatus: 413, wantCode: wire.CodeRequestTooLarge, wantWrite: wire.WriteStateNotSent},
		{name: "missing position", method: "POST", target: "/commands/swap", body: `{}`, wantStatus: 400, wantCode: wire.CodeInvalidRequest, wantWrite: wire.WriteStateNotSent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			r.Header.Set("Neurokit-Spec", "1")

			got := serveAndDecode(t, srv, r)

			if got.status != tt.wantStatus || got.Error.Code != tt.wantCode || got.Write != tt.wantWrite || got.Error.RequestID == "" {
				t.Errorf("%s %s = %d %+v, write %q; want %d %s with a request ID, write %q",
					tt.method, tt.target, got.status, got.Error, got.Write, tt.wantStatus, tt.wantCode, tt.wantWrite)
			}
		})
	}
}

// After a partial deploy, client and broker can be on different spec
// versions. The broker must reply spec-mismatch, so someone rebuilds the
// older image, even though this body (null) is also invalid.
func TestSpecMismatchRejectedBeforeRunner(t *testing.T) {
	srv, _ := newTestServer(t, testManifest, failTestIfRun(t))
	tests := []struct {
		name string
		spec string
	}{
		{name: "client on spec 2", spec: "2"},
		{name: "missing header", spec: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/commands/swap", strings.NewReader("null"))
			if tt.spec != "" {
				r.Header.Set("Neurokit-Spec", tt.spec)
			}

			got := serveAndDecode(t, srv, r)

			messageNamesBothSpecs := strings.Contains(got.Error.Message, `"`+tt.spec+`"`) && strings.Contains(got.Error.Message, "spec 1")
			if got.status != 400 || got.Error.Code != wire.CodeSpecMismatch || got.Write != wire.WriteStateNotSent || !messageNamesBothSpecs {
				t.Errorf("Neurokit-Spec %q = %d %+v, write %q; want 400 spec-mismatch naming %q and spec 1, write not-sent",
					tt.spec, got.status, got.Error, got.Write, tt.spec)
			}
		})
	}
}

// A prompt-injected agent can write any label, so a handler must never see it.
// The handler crashes here because a failed mutation must be audited too.
func TestApprovalLabelAuditedNotPassed(t *testing.T) {
	var stdin []byte
	srv, log := newTestServer(t, testManifest, func(_ context.Context, job broker.Job) broker.RunResult {
		stdin = job.Stdin
		return broker.RunResult{ExitCode: -1}
	})

	got := serveAndDecode(t, srv, newCommandRequest("swap", `{"position":3,"approval":"policy:fantasy-autofix"}`))

	if got.status != 502 || string(stdin) != `{"position":3}` {
		t.Errorf("status %d, handler stdin %s; want 502 and stdin {\"position\":3}", got.status, stdin)
	}
	audit := findLogEntry(t, log, "audit")
	if audit["approval"] != "policy:fantasy-autofix" || audit["code"] != wire.CodeChangeUnknown {
		t.Errorf("audit log entry %v; want approval policy:fantasy-autofix and code change-unknown", audit)
	}
}

// A mutation that times out while waiting for slots never ran, so it must
// reply not-sent and must not run later. It must also leave the line holding
// no slots, or later writes would time out too.
func TestQueuedMutationPastDeadlineNotSent(t *testing.T) {
	lookupRunning := make(chan struct{})
	releaseLookup := make(chan struct{})
	srv, _ := newTestServer(t, shortDeadlines, func(_ context.Context, job broker.Job) broker.RunResult {
		if job.Command == "swap" {
			// Only the retry, after releaseLookup closes, may run.
			select {
			case <-releaseLookup:
				return broker.RunResult{ExitCode: 0, Answer: []byte(`{"result":1,"write":"verified"}`)}
			default:
				t.Error("the swap ran after its deadline passed")
				return broker.RunResult{}
			}
		}
		close(lookupRunning)
		select {
		case <-releaseLookup:
		case <-time.After(time.Second):
		}
		return broker.RunResult{ExitCode: 0, Answer: []byte(`{"result":1}`)}
	})
	// 1. A lookup takes one of the two slots.
	lookupDone := make(chan struct{})
	go func() {
		srv.ServeHTTP(httptest.NewRecorder(), newCommandRequest("lookup", validBody["lookup"]))
		close(lookupDone)
	}()
	<-lookupRunning

	// 2. The swap needs both, so it times out after 50 ms.
	got := serveAndDecode(t, srv, newCommandRequest("swap", validBody["swap"]))
	// 3. With the lookup done, a new swap must get every slot.
	close(releaseLookup)
	<-lookupDone
	retry := serveAndDecode(t, srv, newCommandRequest("swap", validBody["swap"]))

	if got.status != 504 || got.Error.Code != wire.CodeDeadlineExceeded || got.Write != wire.WriteStateNotSent {
		t.Errorf("queued swap = %d %+v, write %q; want 504 deadline-exceeded, write not-sent", got.status, got.Error, got.Write)
	}
	if retry.status != 200 {
		t.Errorf("swap after the lookup finished = %d %+v; want 200, with no slot kept by the timed-out swap", retry.status, retry.Error)
	}
}

// Two writes must never overlap, and max_handlers caps memory: a real handler
// uses about 67 MiB. Four writes also catch two writes waiting on each other.
func TestWriteLockAndSlotsBoundConcurrency(t *testing.T) {
	var mu sync.Mutex
	running := map[string]int{}
	peak, writeOverlapped := 0, false
	srv, _ := newTestServer(t, testManifest, func(_ context.Context, job broker.Job) broker.RunResult {
		mu.Lock()
		running[job.Command]++
		total := running["lookup"] + running["swap"]
		peak = max(peak, total)
		writeOverlapped = writeOverlapped || (running["swap"] > 0 && total > 1)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond) // long enough for other requests to arrive
		mu.Lock()
		running[job.Command]--
		mu.Unlock()
		if job.Mutating {
			return broker.RunResult{ExitCode: 0, Answer: []byte(`{"result":1,"write":"verified"}`)}
		}
		return broker.RunResult{ExitCode: 0, Answer: []byte(`{"result":1}`)}
	})

	var wg sync.WaitGroup
	for i := range 12 {
		command := "lookup"
		if i%3 == 0 {
			command = "swap"
		}
		wg.Go(func() {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, newCommandRequest(command, validBody[command]))
			if rec.Code != 200 {
				t.Errorf("%s = %d %s; want 200", command, rec.Code, rec.Body)
			}
		})
	}
	wg.Wait()

	if peak > 2 || writeOverlapped {
		t.Errorf("peak %d handlers, write overlapped another: %t; want at most 2, and a write alone", peak, writeOverlapped)
	}
}

// A bug in the runner must not drop a mutation's reply and audit log entry,
// because its caller has to know the change may have happened.
func TestRunnerPanicRepliesChangeUnknown(t *testing.T) {
	srv, log := newTestServer(t, testManifest, func(context.Context, broker.Job) broker.RunResult {
		panic("runner bug")
	})

	got := serveAndDecode(t, srv, newCommandRequest("swap", validBody["swap"]))

	if got.status != 502 || got.Error.Code != wire.CodeChangeUnknown || got.Write != wire.WriteStateUnknown {
		t.Errorf("swap = %d %+v, write %q; want 502 change-unknown, write unknown", got.status, got.Error, got.Write)
	}
	if audit := findLogEntry(t, log, "audit"); audit["code"] != wire.CodeChangeUnknown {
		t.Errorf("audit log entry %v; want code change-unknown", audit)
	}
}

// A change must not be made after its caller gave up waiting, and must not
// be cut off halfway because its caller left once the handler started.
func TestHangUpDropsQueuedNotRunning(t *testing.T) {
	lookupRunning := make(chan struct{})
	releaseLookup := make(chan struct{})
	swapsRun := 0
	runningSwap, hangUpRunningSwap := context.WithCancel(context.Background())
	defer hangUpRunningSwap()
	var runErr error
	var runDeadline time.Time
	srv, _ := newTestServer(t, testManifest, func(ctx context.Context, job broker.Job) broker.RunResult {
		if job.Command == "lookup" {
			close(lookupRunning)
			<-releaseLookup
			return broker.RunResult{ExitCode: 0, Answer: []byte(`{"result":1}`)}
		}
		swapsRun++
		hangUpRunningSwap()
		runDeadline, _ = ctx.Deadline()
		runErr = ctx.Err()
		return broker.RunResult{ExitCode: 0, Answer: []byte(`{"result":1,"write":"verified"}`)}
	})
	// 1. A lookup takes one of the two slots, so a swap, which needs both, waits.
	go srv.ServeHTTP(httptest.NewRecorder(), newCommandRequest("lookup", validBody["lookup"]))
	<-lookupRunning

	// 2. The waiting swap's client hangs up.
	ctx, hangUp := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, hangUp)
	queued := serveAndDecode(t, srv, newCommandRequest("swap", validBody["swap"]).WithContext(ctx))
	swapsRunWhileQueued := swapsRun
	// 3. A new swap waits 50 ms for the lookup, then its client hangs up while
	// its handler runs.
	sent := time.Now()
	time.AfterFunc(50*time.Millisecond, func() { close(releaseLookup) })
	running := serveAndDecode(t, srv, newCommandRequest("swap", validBody["swap"]).WithContext(runningSwap))

	if queued.status != 503 || queued.Error.Code != wire.CodeNotReady || queued.Write != wire.WriteStateNotSent || swapsRunWhileQueued != 0 {
		t.Errorf("queued swap = %d %+v, write %q, %d run; want 503 not-ready, write not-sent, none run",
			queued.status, queued.Error, queued.Write, swapsRunWhileQueued)
	}
	if running.status != 200 || runErr != nil || runDeadline.IsZero() || runDeadline.Sub(sent) > 5*time.Second+25*time.Millisecond {
		t.Errorf("running swap = %d, handler context error %v, deadline %v after sending; want 200, no error, the 5 s set on arrival",
			running.status, runErr, runDeadline.Sub(sent))
	}
}
