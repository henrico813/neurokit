// Package runner runs one handler and returns only after killing its
// process group, so a hung handler or its children cannot outlive the request.
package runner

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// After SIGTERM at the deadline, the handler has this long to exit before
	// SIGKILL.
	gracePeriod = 2 * time.Second
	// A process that left the group can hold a pipe open for as long as it
	// lives, so reading stops this long after the group is killed.
	drainLimit     = 200 * time.Millisecond
	maxOutputBytes = 64 << 10
)

type Process struct {
	Argv           []string
	Env            []string
	Stdin          []byte
	MaxAnswerBytes int64
	Log            *slog.Logger
}

type Result struct {
	ExitCode       int
	Answer         []byte
	LastStderrLine string
}

func Run(ctx context.Context, p Process) Result {
	tmpDir, err := os.MkdirTemp("", "neurokit-")
	if err != nil {
		return reportStartFailure(p.Log, err)
	}
	// Deferred first, so it runs last, after the group is killed.
	defer os.RemoveAll(tmpDir)
	readEnds, writeEnds, err := openPipes(4)
	if err != nil {
		return reportStartFailure(p.Log, err)
	}
	stdinR, stdoutR, stderrR, answerR := readEnds[0], readEnds[1], readEnds[2], readEnds[3]
	stdinW, stdoutW, stderrW, answerW := writeEnds[0], writeEnds[1], writeEnds[2], writeEnds[3]
	defer closeFiles(stdinW, stdoutR, stderrR, answerR)

	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	cmd.Env = append(append(os.Environ(), p.Env...), "TMPDIR="+tmpDir)
	// Pass the pipes as files, not readers or writers, so Wait does not also
	// wait for a leftover child that still holds one.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	cmd.ExtraFiles = []*os.File{answerW} // the handler sees the answer pipe as fd 3
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = gracePeriod
	err = cmd.Start()
	// The child has its own copies now, and a read ends only once every copy
	// is closed.
	closeFiles(stdinR, stdoutW, stderrW, answerW)
	if err != nil {
		return reportStartFailure(p.Log, err)
	}
	// Deferred too, so a panic in Run cannot leave the group running.
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	var readers sync.WaitGroup
	var answer []byte
	var lastStderrLine string
	var outputLeft, outputDropped atomic.Int64
	outputLeft.Store(maxOutputBytes)
	readers.Go(func() {
		answer, _ = io.ReadAll(io.LimitReader(answerR, p.MaxAnswerBytes+1))
		// A handler that writes past the cap gets a write error instead of blocking.
		answerR.Close()
	})
	readers.Go(func() { logHandlerLines(stdoutR, "stdout", p.Log, &outputLeft, &outputDropped) })
	readers.Go(func() { lastStderrLine = logHandlerLines(stderrR, "stderr", p.Log, &outputLeft, &outputDropped) })
	// Not waited for: closing stdinW below ends the write if the handler never
	// reads its stdin.
	go func() {
		stdinW.Write(p.Stdin)
		stdinW.Close()
	}()

	cmd.Wait()
	// Kill the group even after a clean exit: nothing the handler started
	// should outlive its request.
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	drained := make(chan struct{})
	go func() {
		readers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(drainLimit):
		p.Log.Warn("handler_pipe_held", "detail", "a process outside the handler's group still holds a pipe")
	}
	closeFiles(stdinW, stdoutR, stderrR, answerR)
	<-drained
	if dropped := outputDropped.Load(); dropped > 0 {
		p.Log.Warn("handler_output_dropped", "bytes", dropped)
	}
	return Result{ExitCode: cmd.ProcessState.ExitCode(), Answer: answer, LastStderrLine: lastStderrLine}
}

// Logs lines until the run's output cap is spent, then only counts them.
// Returns the last line, because a failing program's reason usually comes last.
func logHandlerLines(r io.Reader, stream string, log *slog.Logger, left, dropped *atomic.Int64) (lastLine string) {
	lines := bufio.NewReaderSize(r, 4096)
	for {
		// A line longer than the buffer is logged in pieces.
		line, err := lines.ReadSlice('\n')
		if text := strings.TrimRight(string(line), "\r\n"); text != "" {
			if left.Add(-int64(len(line))) >= 0 {
				log.Info("handler_output", "stream", stream, "line", text)
			} else {
				dropped.Add(int64(len(line)))
			}
			lastLine = text
		}
		if err != nil && err != bufio.ErrBufferFull {
			return lastLine
		}
	}
}

func openPipes(n int) (readEnds, writeEnds []*os.File, err error) {
	for range n {
		r, w, err := os.Pipe()
		if err != nil {
			closeFiles(append(readEnds, writeEnds...)...)
			return nil, nil, err
		}
		readEnds, writeEnds = append(readEnds, r), append(writeEnds, w)
	}
	return readEnds, writeEnds, nil
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		f.Close()
	}
}

func reportStartFailure(log *slog.Logger, err error) Result {
	log.Error("handler_start_failed", "error", err)
	return Result{ExitCode: -1, LastStderrLine: err.Error()}
}
