package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/henrico813/neurokit/internal/runner"
)

const maxAnswerBytes = 100

// Runs a shell script as the handler. In the script, >&3 writes to the answer
// pipe, which is kept apart from the handler's log output. A script that
// starts a child saves its process ID in $PID_FILE, so the test can check it
// was killed.
func runScript(
	t *testing.T,
	ctx context.Context,
	script string,
	env ...string,
) (
	runner.Result,
	string,
	*bytes.Buffer,
) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	var log bytes.Buffer
	result := runner.Run(ctx, runner.Process{
		Argv:           []string{"sh", "-c", script},
		Env:            append(env, "PID_FILE="+pidFile),
		Stdin:          []byte(`{"query":"kim"}`),
		MaxAnswerBytes: maxAnswerBytes,
		Log:            slog.New(slog.NewJSONHandler(&log, nil)),
	})
	return result, pidFile, &log
}

func readPID(t *testing.T, pidFile string) int {
	t.Helper()
	text, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// A killed process stays a zombie until its parent cleans it up, so a zombie
// counts as ended.
func isRunning(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))[0] != "Z"
}

// SIGKILL takes a moment to land, so this waits up to a second.
func waitForProcessExit(pid int) bool {
	for range 100 {
		if !isRunning(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func findLogEntries(log *bytes.Buffer, msg string) []map[string]any {
	var entries []map[string]any
	for line := range strings.Lines(log.String()) {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == msg {
			entries = append(entries, entry)
		}
	}
	return entries
}

// A real tool logs to stdout on every call, so when a prototype read answers
// from stdout, every one of that tool's answers failed to decode.
func TestStdoutNoiseDoesNotCorruptAnswer(t *testing.T) {
	result, _, log := runScript(
		t,
		context.Background(),
		`echo "scout: analyzing"; cat >&3`,
	)

	if string(result.Answer) != `{"query":"kim"}` || result.ExitCode != 0 {
		t.Errorf(
			"answer %q, exit %d; want the handler's stdin echoed on the answer pipe, exit 0",
			result.Answer,
			result.ExitCode,
		)
	}
	output := findLogEntries(log, "handler_output")
	if len(output) != 1 || output[0]["stream"] != "stdout" || output[0]["line"] != "scout: analyzing" {
		t.Errorf(
			"handler_output entries %v; want one stdout entry for the print",
			output,
		)
	}
}

// A hung handler, or an ssh that git started, must not outlive its request
// or fill the container's small /tmp, even when it ignores SIGTERM.
func TestDeadlineKillsWholeProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		100*time.Millisecond,
	)
	defer cancel()
	start := time.Now()

	result, pidFile, _ := runScript(
		t,
		ctx,
		`trap '' TERM
		touch "$TMPDIR/scratch"
		sleep 30 &
		echo $! >"$PID_FILE"
		printf '"%s"' "$TMPDIR" >&3
		wait`,
	)

	var tmpDir string
	json.Unmarshal(result.Answer, &tmpDir)
	if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 3*time.Second || result.ExitCode != -1 {
		t.Errorf(
			"Run took %v, exit %d; want the deadline plus the 2 s grace period, exit -1",
			elapsed,
			result.ExitCode,
		)
	}
	if !waitForProcessExit(readPID(t, pidFile)) {
		t.Error("the handler's child is still running")
	}
	if _, err := os.Stat(tmpDir); !strings.HasPrefix(tmpDir, os.TempDir()+"/") || !os.IsNotExist(err) {
		t.Errorf(
			"handler TMPDIR %q: %v; want a private directory under %s, removed",
			tmpDir,
			err,
			os.TempDir(),
		)
	}
}

// A child left running after a clean exit once kept the request open for
// the whole grace period, blocking every other command during a write.
func TestLeftoverChildKilledAnswerTrusted(t *testing.T) {
	start := time.Now()

	result, pidFile, log := runScript(
		t,
		context.Background(),
		`sleep 30 &
		echo $! >"$PID_FILE"
		echo '{"result":1}' >&3`,
	)

	if elapsed := time.Since(start); elapsed > time.Second || string(result.Answer) != "{\"result\":1}\n" {
		t.Errorf(
			"Run took %v, answer %q; want well under 1 s and the answer",
			elapsed,
			result.Answer,
		)
	}
	if held := findLogEntries(log, "handler_pipe_held"); len(held) != 0 {
		t.Errorf(
			"handler_pipe_held logged %v; want none, since killing the group closes every pipe",
			held,
		)
	}
	if !waitForProcessExit(readPID(t, pidFile)) {
		t.Error("the leftover child is still running")
	}
}

// A daemon that leaves the group escapes the kill and can keep the answer pipe
// open as long as it lives. Without a bound on reading, it holds the request
// open, and other requests can wait behind it.
func TestAnswerPipeHolderCannotStallRequest(t *testing.T) {
	start := time.Now()

	// The handler answers only once the daemon has left the group, so the
	// group kill cannot reach it first.
	result, pidFile, log := runScript(
		t,
		context.Background(),
		`setsid sh -c 'echo $$ >"$PID_FILE"; exec sleep 5' &
		while [ ! -s "$PID_FILE" ]; do sleep 0.01; done
		echo '{"result":1}' >&3`,
	)

	daemon := readPID(t, pidFile)
	t.Cleanup(func() { syscall.Kill(daemon, syscall.SIGKILL) })
	if elapsed := time.Since(start); elapsed > time.Second || string(result.Answer) != "{\"result\":1}\n" {
		t.Errorf(
			"Run took %v, answer %q; want well under 1 s and the answer",
			elapsed,
			result.Answer,
		)
	}
	if !isRunning(daemon) {
		t.Error("the daemon ended, so this test no longer covers a held pipe")
	}
	if len(findLogEntries(log, "handler_pipe_held")) != 1 {
		t.Errorf("no handler_pipe_held log entry in:\n%s", log)
	}
}

// An answer too large to trust must reach the broker as more than the cap, not
// as a cut-off answer that might parse, and the handler must not block on it.
func TestOversizedAnswerStopsHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()

	result, _, _ := runScript(t, ctx, `head -c 10000000 /dev/zero >&3`)

	if elapsed := time.Since(start); elapsed > time.Second || len(result.Answer) != maxAnswerBytes+1 {
		t.Errorf(
			"Run took %v, answer %d bytes; want well under 1 s and %d bytes",
			elapsed,
			len(result.Answer),
			maxAnswerBytes+1,
		)
	}
}

// A chatty handler must not fill the broker's log. The last stderr line is
// kept past the cap, because serve reports it when the startup check fails.
func TestHandlerOutputLoggedAndCapped(t *testing.T) {
	result, _, log := runScript(
		t,
		context.Background(),
		`yes "$(printf '%099d' 0)" | head -n 2000 >&2
		echo 'GEMINI_API_KEY is required' >&2`,
	)

	loggedBytes := 0
	for _, entry := range findLogEntries(log, "handler_output") {
		loggedBytes += len(entry["line"].(string)) + 1
	}
	dropped := findLogEntries(log, "handler_output_dropped")
	if loggedBytes > 64<<10 || len(dropped) != 1 || dropped[0]["bytes"].(float64) < 130_000 {
		t.Errorf(
			"logged %d bytes, dropped entries %v; want at most 64 KiB logged and one entry for the rest",
			loggedBytes,
			dropped,
		)
	}
	if result.LastStderrLine != "GEMINI_API_KEY is required" {
		t.Errorf(
			"last stderr line %q; want the line printed last",
			result.LastStderrLine,
		)
	}
}

// Handlers get their secrets from the container's environment, and the
// variables the broker adds must win over stale ones of the same name.
func TestHandlerInheritsEnvironment(t *testing.T) {
	t.Setenv("DEMO_API_KEY", "from-compose")
	t.Setenv("NEUROKIT_COMMAND", "stale")

	result, _, _ := runScript(
		t,
		context.Background(),
		`printf '["%s","%s"]' "$DEMO_API_KEY" "$NEUROKIT_COMMAND" >&3`,
		"NEUROKIT_COMMAND=lookup",
	)

	if string(result.Answer) != `["from-compose","lookup"]` {
		t.Errorf(
			"handler saw %s; want the inherited key and the added command",
			result.Answer,
		)
	}
}
