package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/henrico813/neurokit/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, cli.Usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServeCommand(os.Args[2:]))
	case "run":
		os.Exit(cli.Run(
			os.Args[2:],
			readVersion(),
			os.Stdout,
			os.Stderr,
		))
	case "call":
		os.Exit(cli.Call(os.Args[2:], os.Stdout, os.Stderr))
	case "help":
		os.Exit(cli.Help(
			os.Args[2:],
			readVersion(),
			os.Stdout,
			os.Stderr,
		))
	case "health":
		os.Exit(cli.Health(os.Args[2:], os.Stdout, os.Stderr))
	}
	fmt.Fprintln(os.Stderr, cli.Usage)
	os.Exit(2)
}

func runServeCommand(args []string) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// Port 0 takes a free port, which the serve_start log entry shows as addr.
	listenAddr := flags.String("listen", "0.0.0.0:8080", "")
	if flags.Parse(args) != nil || flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: neurokit serve [--listen <host:port>] <manifest>")
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
		return net.Listen("tcp", *listenAddr)
	}
	if err := serveBroker(stop, flags.Arg(0), listen, log); err != nil {
		fmt.Fprintln(os.Stderr, "neurokit serve:", err)
		return 1
	}
	return 0
}
