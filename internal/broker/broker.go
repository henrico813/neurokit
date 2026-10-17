// Package broker is the HTTP server in a tool's container: it checks each
// request against the manifest and runs the tool's handler at most once per
// request. The README's Terms explain the project's words.
package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/henrico813/neurokit/internal/manifest"
	"github.com/henrico813/neurokit/internal/wire"
)

type Job struct {
	Command          string
	RequestID        string
	Mutating         bool
	Stdin            []byte
	MaxResponseBytes int64
}

type RunResult struct {
	ExitCode int
	Answer   []byte
}

type Runner interface {
	Run(ctx context.Context, job Job) RunResult
}

type Server struct {
	manifest    *manifest.Manifest
	rawManifest []byte
	runner      Runner
	checkReady  func(context.Context) error
	log         *slog.Logger
	// Each handler uses memory, so at most max_handlers run at once, one per
	// slot. A request takes all the slots it needs in one step, in arrival
	// order.
	slots *semaphore.Weighted
}

func New(
	m *manifest.Manifest,
	rawManifest []byte,
	runner Runner,
	checkReady func(context.Context) error,
	log *slog.Logger,
) *Server {
	return &Server{
		manifest:    m,
		rawManifest: rawManifest,
		runner:      runner,
		checkReady:  checkReady,
		log:         log,
		slots:       semaphore.NewWeighted(int64(m.MaxHandlers)),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := generateRequestID()
	commandName, isCommand := strings.CutPrefix(
		r.URL.Path,
		wire.CommandRoutePrefix,
	)
	var cmd *manifest.Command
	if isCommand {
		cmd = s.manifest.Commands[commandName]
	} else {
		commandName = ""
	}
	var reply callerReply
	switch {
	case cmd != nil:
		reply = s.checkAndRunCommand(r, requestID, commandName, cmd)
	case isCommand || !slices.Contains(
		[]string{
			wire.LivezRoute,
			wire.ReadyzRoute,
			wire.ManifestRoute,
		},
		r.URL.Path,
	):
		reply = callerReply{
			code:    wire.CodeNotFound,
			message: fmt.Sprintf("no route %q", r.URL.Path),
		}
	case r.Method != http.MethodGet:
		reply = callerReply{
			code:    wire.CodeMethodNotAllowed,
			message: r.URL.Path + " takes GET",
		}
	case r.URL.Path == wire.ManifestRoute:
		w.Header().Set("Content-Type", "application/json")
		w.Write(s.rawManifest)
		return
	case r.URL.Path == wire.ReadyzRoute:
		reply = callerReply{result: json.RawMessage(`"ok"`)}
		if err := s.checkReady(r.Context()); err != nil {
			reply = callerReply{
				code:    wire.CodeNotReady,
				message: "the broker is not ready; its log has details",
				detail:  err.Error(),
			}
		}
	default:
		reply = callerReply{result: json.RawMessage(`"ok"`)}
	}
	writeReply(w, requestID, reply)
	// Health checks arrive every few seconds, so successful GETs are not logged.
	if isCommand || reply.code != "" {
		s.log.Info(
			"request",
			"request_id", requestID,
			"command", commandName,
			"path", r.URL.Path,
			"code", reply.code,
			"write", reply.write,
			"duration_ms", time.Since(start).Milliseconds(),
			"error", reply.detail,
		)
	}
}

func (s *Server) checkAndRunCommand(
	r *http.Request,
	requestID,
	name string,
	cmd *manifest.Command,
) callerReply {
	stdin, approvalLabel, refusal := s.checkCommandRequest(r, cmd)
	if refusal != nil {
		return *refusal
	}

	// Waiting for slots counts against the deadline, and a client that hangs up
	// while waiting cancels its request before anything runs.
	ctx, cancel := context.WithTimeout(
		r.Context(),
		time.Duration(cmd.DeadlineMS)*time.Millisecond,
	)
	defer cancel()
	// A lock: write command takes every slot, so it runs alone.
	slotsNeeded := int64(1)
	if cmd.Lock == "write" {
		slotsNeeded = int64(s.manifest.MaxHandlers)
	}
	if err := s.slots.Acquire(ctx, slotsNeeded); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return *refuseBeforeRun(
				cmd,
				wire.CodeDeadlineExceeded,
				"the deadline passed before the command could start",
			)
		}
		return *refuseBeforeRun(
			cmd,
			wire.CodeNotReady,
			"the command did not start: its client hung up or the broker is stopping",
		)
	}
	defer s.slots.Release(slotsNeeded)
	// Once started, a handler finishes even if its client hangs up, so a
	// change is never cut off halfway. WithoutCancel drops the deadline, so it
	// is set again.
	deadline, _ := ctx.Deadline()
	runCtx, cancelRun := context.WithDeadline(
		context.WithoutCancel(ctx),
		deadline,
	)
	defer cancelRun()
	handlerRun := s.runAndRecoverPanic(runCtx, Job{
		Command:          name,
		RequestID:        requestID,
		Mutating:         cmd.Mutating,
		Stdin:            stdin,
		MaxResponseBytes: cmd.MaxResponseBytes,
	})
	reply := buildReplyFromHandlerRun(cmd, handlerRun, runCtx.Err() != nil)
	if cmd.Mutating {
		s.log.Info(
			"audit",
			"request_id", requestID,
			"command", name,
			"approval", approvalLabel,
			"code", reply.code,
			"write", reply.write,
		)
	}
	return reply
}

// A panic counts as a crash, so a mutation still replies change-unknown and
// gets its audit log entry, instead of a dropped connection.
func (s *Server) runAndRecoverPanic(
	ctx context.Context,
	job Job,
) (handlerRun RunResult) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error(
				"runner_panic",
				"request_id", job.RequestID,
				"panic", fmt.Sprint(p),
				"stack", string(debug.Stack()),
			)
			handlerRun = RunResult{ExitCode: -1}
		}
	}()
	return s.runner.Run(ctx, job)
}

func (s *Server) checkCommandRequest(
	r *http.Request,
	cmd *manifest.Command,
) (
	stdin []byte,
	approvalLabel string,
	refusal *callerReply,
) {
	// Check the spec before the body, so a client on another version is told
	// to rebuild, not that its body is wrong.
	if r.Method != http.MethodPost {
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeMethodNotAllowed,
			"commands take POST",
		)
	}
	if r.URL.RawQuery != "" {
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeInvalidRequest,
			"commands take no query string",
		)
	}
	if spec := r.Header.Get(wire.SpecHeader); spec != strconv.Itoa(s.manifest.Spec) {
		message := fmt.Sprintf(
			"client spec %q does not match broker spec %d",
			spec,
			s.manifest.Spec,
		)
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeSpecMismatch,
			message,
		)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, cmd.MaxBodyBytes+1))
	if err != nil {
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeInvalidRequest,
			"could not read the body",
		)
	}
	if int64(len(body)) > cmd.MaxBodyBytes {
		message := fmt.Sprintf("body is larger than %d bytes", cmd.MaxBodyBytes)
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeRequestTooLarge,
			message,
		)
	}
	args, approvalLabel, err := cmd.DecodeAndValidateArgs(body)
	if err != nil {
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeInvalidRequest,
			err.Error(),
		)
	}
	stdin, err = json.Marshal(args)
	if err != nil {
		return nil, "", refuseBeforeRun(
			cmd,
			wire.CodeUnexpected,
			"could not encode the arguments",
		)
	}
	return stdin, approvalLabel, nil
}

func generateRequestID() string {
	b := make([]byte, 8)
	rand.Read(b) // never returns an error since Go 1.24
	return "req-" + hex.EncodeToString(b)
}
