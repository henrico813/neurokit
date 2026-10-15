// Package broker is the HTTP server in a tool's container: it checks each
// request against the manifest and runs the tool's handler at most once per
// request. The README's Terms explain the project's words.
package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	log         *slog.Logger
	// Each handler uses memory, so at most max_handlers run at once, one per
	// slot. A request takes all the slots it needs in one step, in arrival
	// order.
	slots *semaphore.Weighted
}

func New(m *manifest.Manifest, rawManifest []byte, runner Runner, log *slog.Logger) *Server {
	return &Server{
		manifest:    m,
		rawManifest: rawManifest,
		runner:      runner,
		log:         log,
		slots:       semaphore.NewWeighted(int64(m.MaxHandlers)),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := generateRequestID()
	commandName, isCommand := strings.CutPrefix(r.URL.Path, "/commands/")
	var cmd *manifest.Command
	if isCommand {
		cmd = s.manifest.Commands[commandName]
	}
	var reply callerReply
	switch {
	case cmd != nil:
		reply = s.checkAndRunCommand(r, requestID, commandName, cmd)
	case isCommand || !slices.Contains([]string{"/livez", "/readyz", "/manifest"}, r.URL.Path):
		reply = callerReply{code: wire.CodeNotFound, message: fmt.Sprintf("no route %q", r.URL.Path)}
	case r.Method != http.MethodGet:
		reply = callerReply{code: wire.CodeMethodNotAllowed, message: r.URL.Path + " takes GET"}
	case r.URL.Path == "/manifest":
		w.Header().Set("Content-Type", "application/json")
		w.Write(s.rawManifest)
		return
	default:
		reply = callerReply{result: json.RawMessage(`"ok"`)}
	}
	writeReply(w, requestID, reply)
	// Health checks arrive every few seconds, so successful GETs are not logged.
	if isCommand || reply.code != "" {
		s.log.Info("request", "request_id", requestID, "path", r.URL.Path, "code", reply.code, "write", reply.write,
			"duration_ms", time.Since(start).Milliseconds(), "detail", reply.detail)
	}
}

func (s *Server) checkAndRunCommand(r *http.Request, requestID, name string, cmd *manifest.Command) callerReply {
	stdin, approvalLabel, refusal := s.checkCommandRequest(r, cmd)
	if refusal != nil {
		return *refusal
	}

	// Waiting for slots counts against the deadline. A client hanging up does
	// not stop the handler.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Duration(cmd.DeadlineMS)*time.Millisecond)
	defer cancel()
	// A lock: write command takes every slot, so it runs alone.
	slotsNeeded := int64(1)
	if cmd.Lock == "write" {
		slotsNeeded = int64(s.manifest.MaxHandlers)
	}
	if err := s.slots.Acquire(ctx, slotsNeeded); err != nil {
		return *refuseBeforeRun(cmd, wire.CodeDeadlineExceeded, "the deadline passed before the command could start")
	}
	defer s.slots.Release(slotsNeeded)
	handlerRun := s.runner.Run(ctx, Job{
		Command:          name,
		RequestID:        requestID,
		Mutating:         cmd.Mutating,
		Stdin:            stdin,
		MaxResponseBytes: cmd.MaxResponseBytes,
	})
	reply := buildReplyFromHandlerRun(cmd, handlerRun, ctx.Err() != nil)
	if cmd.Mutating {
		s.log.Info("audit", "request_id", requestID, "command", name, "approval", approvalLabel, "code", reply.code, "write", reply.write)
	}
	return reply
}

func (s *Server) checkCommandRequest(r *http.Request, cmd *manifest.Command) (stdin []byte, approvalLabel string, refusal *callerReply) {
	// Check the spec before the body, so a client on another version is told
	// to rebuild, not that its body is wrong.
	if r.Method != http.MethodPost {
		return nil, "", refuseBeforeRun(cmd, wire.CodeMethodNotAllowed, "commands take POST")
	}
	if r.URL.RawQuery != "" {
		return nil, "", refuseBeforeRun(cmd, wire.CodeInvalidRequest, "commands take no query string")
	}
	if spec := r.Header.Get("Neurokit-Spec"); spec != strconv.Itoa(s.manifest.Spec) {
		message := fmt.Sprintf("client spec %q does not match broker spec %d", spec, s.manifest.Spec)
		return nil, "", refuseBeforeRun(cmd, wire.CodeSpecMismatch, message)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, cmd.MaxBodyBytes+1))
	if err != nil {
		return nil, "", refuseBeforeRun(cmd, wire.CodeInvalidRequest, "could not read the body")
	}
	if int64(len(body)) > cmd.MaxBodyBytes {
		message := fmt.Sprintf("body is larger than %d bytes", cmd.MaxBodyBytes)
		return nil, "", refuseBeforeRun(cmd, wire.CodeRequestTooLarge, message)
	}
	args, err := cmd.DecodeAndValidateArgs(body)
	if err != nil {
		return nil, "", refuseBeforeRun(cmd, wire.CodeInvalidRequest, err.Error())
	}
	// The handler never gets the label, so a tool cannot act differently
	// because of what an agent wrote there.
	approvalLabel, _ = args["approval"].(string)
	delete(args, "approval")
	stdin, err = json.Marshal(args)
	if err != nil {
		return nil, "", refuseBeforeRun(cmd, wire.CodeUnexpected, "could not encode the arguments")
	}
	return stdin, approvalLabel, nil
}

func generateRequestID() string {
	b := make([]byte, 8)
	rand.Read(b) // never returns an error since Go 1.24
	return "req-" + hex.EncodeToString(b)
}
