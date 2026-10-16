package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/henrico813/neurokit/internal/broker"
	"github.com/henrico813/neurokit/internal/manifest"
	"github.com/henrico813/neurokit/internal/runner"
)

const (
	readTimeout = 10 * time.Second
	readyLimit  = time.Second
)

// Returns a one-line reason, before listening, when the manifest or the tool's
// check program is wrong, or when serve is the container's first process.
// Once stop ends, returns after every reply is written, or at most the grace
// period plus 1 s later.
func serveBroker(stop context.Context, manifestPath string, listen func() (net.Listener, error), log *slog.Logger) error {
	m, err := manifest.ReadAndValidate(manifestPath)
	if err != nil {
		return err
	}
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if os.Getpid() == 1 {
		return errors.New("refusing to run as PID 1, where finished handler processes are never cleaned up; start the container with an init process (docker run --init, or init: true in Compose)")
	}
	handlers := handlerRunner{manifestPath: manifestPath, manifest: m, stop: stop, log: log}
	// The check runs the way handlers do, so an unwritable /tmp fails here, not
	// on the first request.
	if err := handlers.runCheckProgram(stop, "check", m.Check); err != nil {
		return err
	}
	log.Info("serve_start", "version", readVersion(), "spec", m.Spec)
	ln, err := listen()
	if err != nil {
		return err
	}

	srv := buildHTTPServer(m, broker.New(m, rawManifest, handlers, handlers.checkReady, log), stop)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-stop.Done():
	}
	// By now stop has dropped requests still waiting to run and is stopping
	// running handlers, so Shutdown only waits for their replies.
	log.Info("serve_stop")
	shutdown, cancel := context.WithTimeout(context.Background(), runner.GracePeriod+time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Warn("serve_stop_cut_short", "error", err)
	}
	return nil
}

// Go starts the write timeout once a request's headers are read, so it must
// also cover reading the body, waiting for other handlers to finish, and the
// handler with its grace period. A shorter one could cut off a reply that
// confirms a change.
func buildHTTPServer(m *manifest.Manifest, handler http.Handler, stop context.Context) *http.Server {
	var longestDeadlineMS int64
	for _, cmd := range m.Commands {
		longestDeadlineMS = max(longestDeadlineMS, cmd.DeadlineMS)
	}
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      time.Duration(longestDeadlineMS)*time.Millisecond + runner.GracePeriod + readTimeout + time.Second,
		// Every request's context comes from stop, so a request still waiting
		// for its turn to run is dropped when serve stops.
		BaseContext: func(net.Listener) context.Context { return stop },
	}
}

func readVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return info.Main.Version
}

type handlerRunner struct {
	manifestPath string
	manifest     *manifest.Manifest
	stop         context.Context
	log          *slog.Logger
}

func (h handlerRunner) Run(ctx context.Context, job broker.Job) broker.RunResult {
	// The broker runs a started handler on a context that a hang-up cannot
	// cancel, so a stop has to cancel it here.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopWatching := context.AfterFunc(h.stop, cancel)
	defer stopWatching()
	deadline, _ := ctx.Deadline()
	mutating := "0"
	if job.Mutating {
		mutating = "1"
	}
	result := runner.Run(ctx, runner.Process{
		Argv: h.manifest.Program,
		Env: []string{
			"NEUROKIT_MANIFEST=" + h.manifestPath,
			"NEUROKIT_COMMAND=" + job.Command,
			"NEUROKIT_REQUEST_ID=" + job.RequestID,
			"NEUROKIT_DEADLINE_MS=" + strconv.FormatInt(time.Until(deadline).Milliseconds(), 10),
			"NEUROKIT_MUTATING=" + mutating,
		},
		Stdin:          job.Stdin,
		MaxAnswerBytes: job.MaxResponseBytes,
		// One search for a request ID then finds every line its handler logged.
		Log: h.log.With("request_id", job.RequestID, "command", job.Command),
	})
	return broker.RunResult{ExitCode: result.ExitCode, Answer: result.Answer}
}

// Health checks call this every few seconds, so a slow ready program is
// stopped after 1 s.
func (h handlerRunner) checkReady(ctx context.Context) error {
	if len(h.manifest.Ready) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, readyLimit)
	defer cancel()
	return h.runCheckProgram(ctx, "ready", h.manifest.Ready)
}

// When the program fails, returns its last stderr line as the reason, such as
// "check failed: GEMINI_API_KEY is required".
func (h handlerRunner) runCheckProgram(ctx context.Context, name string, argv []string) error {
	result := runner.Run(ctx, runner.Process{
		Argv: argv,
		Env:  []string{"NEUROKIT_MANIFEST=" + h.manifestPath},
		Log:  h.log.With("command", name),
	})
	switch {
	case result.ExitCode == 0:
		return nil
	case result.LastStderrLine == "":
		return fmt.Errorf("%s exited %d with nothing on stderr", name, result.ExitCode)
	}
	return fmt.Errorf("%s failed: %s", name, result.LastStderrLine)
}
