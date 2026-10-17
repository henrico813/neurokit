package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/henrico813/neurokit/internal/cli"
)

const usage = "usage: neurokit serve|call <manifest> ..."

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServeCommand(os.Args[2:]))
	case "call":
		os.Exit(cli.Call(os.Args[2:], os.Stdout, os.Stderr))
	}
	fmt.Fprintln(os.Stderr, usage)
	os.Exit(2)
}

func runServeCommand(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: neurokit serve <manifest>")
		return 2
	}
	// docker stop sends SIGTERM, and Ctrl-C in a terminal sends an interrupt;
	// both stop serve the same way.
	stop, _ := signal.NotifyContext(
		context.Background(),
		syscall.SIGTERM,
		os.Interrupt,
	)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	listen := func() (net.Listener, error) {
		return net.Listen("tcp", "0.0.0.0:8080")
	}
	if err := serveBroker(stop, args[0], listen, log); err != nil {
		fmt.Fprintln(os.Stderr, "neurokit serve:", err)
		return 1
	}
	return 0
}
