package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"symterm/internal/buildinfo"
	daemoncmd "symterm/internal/cmd/daemon"
	"symterm/internal/config"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "internal" && os.Args[2] == "tmux-status" {
		if err := daemoncmd.RunInternalTmuxStatus(context.Background(), os.Args[3:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "symtermd: %v\n", err)
			os.Exit(2)
		}
		return
	}

	fs := flag.NewFlagSet("symtermd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var verbose bool
	fs.BoolVar(&verbose, "v", false, "print detailed daemon-side tracing to stderr")
	fs.BoolVar(&verbose, "verbose", false, "print detailed daemon-side tracing to stderr")
	showVersion := fs.Bool("version", false, "print build version")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stdout, daemonUsage())
			return
		}
		fmt.Fprintf(os.Stderr, "symtermd: %v\n", err)
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "symtermd: unexpected arguments: %v\n", fs.Args())
		os.Exit(2)
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, buildinfo.ResolveVersion())
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "symtermd: resolve home: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.LoadDaemonConfig(config.EnvMap(os.Environ()), home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "symtermd: %v\n", err)
		os.Exit(2)
	}
	if verbose {
		logger := log.New(os.Stderr, "symterm trace: ", log.LstdFlags|log.Lmicroseconds)
		cfg.Tracef = logger.Printf
	}

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemoncmd.Run(runCtx, cfg, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "symtermd: %v\n", err)
		os.Exit(3)
	}
	// Allow any FUSE server threads that received abort/ENODEV a moment to
	// fully exit their read(2) syscalls before the runtime invokes exit_group.
	time.Sleep(500 * time.Millisecond)
}

func daemonUsage() string {
	return `Usage: symtermd [options]

Options:
  -v, --verbose
        print detailed daemon-side tracing to stderr
      --version
        print build version
  -h, --help
        show daemon help
`
}
