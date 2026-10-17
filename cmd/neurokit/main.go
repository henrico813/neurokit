// Command neurokit is the broker in a tool's container: neurokit serve
// <manifest> checks each request against the manifest and runs the tool's
// handler for it.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: neurokit serve <manifest>")
		os.Exit(2)
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
	if err := serveBroker(stop, os.Args[2], listen, log); err != nil {
		fmt.Fprintln(os.Stderr, "neurokit serve:", err)
		os.Exit(1)
	}
}
