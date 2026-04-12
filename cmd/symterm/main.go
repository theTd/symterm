package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"symterm/internal/buildinfo"
	adminclient "symterm/internal/cmd/adminclient"
	clientcmd "symterm/internal/cmd/client"
	setupwizard "symterm/internal/cmd/setupwizard"
	"symterm/internal/config"
)

func main() {
	args := os.Args[1:]
	switch classifyCommandRoute(args) {
	case routeHelp:
		fmt.Fprintln(os.Stdout, rootUsage())
		return
	case routeVersion:
		fmt.Fprintln(os.Stdout, buildinfo.ResolveVersion())
		return
	case routeAuthorityBroker:
		if err := clientcmd.RunAuthorityBroker(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "symterm authority broker: %v\n", err)
			os.Exit(3)
		}
		return
	case routeAdmin:
		if err := adminclient.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "symterm: %v\n", err)
			os.Exit(3)
		}
		return
	case routeSetup:
		if err := setupwizard.Run(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr, config.EnvMap(os.Environ())); err != nil {
			fmt.Fprintf(os.Stderr, "symterm setup: %v\n", err)
			os.Exit(3)
		}
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "symterm: resolve cwd: %v\n", err)
		os.Exit(1)
	}

	env := config.EnvMap(os.Environ())
	var cfg config.ClientConfig
	switch classifyCommandRoute(args) {
	case routeRun:
		cfg, err = config.ParseClientConfig(os.Args[2:], env, cwd)
	case routeDefault:
		cfg, err = config.ParseDefaultClientConfig(args, env, cwd)
	default:
		fmt.Fprintln(os.Stdout, rootUsage())
		return
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if classifyCommandRoute(args) == routeRun {
				fmt.Fprintln(os.Stdout, config.ClientUsage())
			} else {
				fmt.Fprintln(os.Stdout, rootUsage())
			}
			return
		}
		fmt.Fprintf(os.Stderr, "symterm: %v\n", err)
		os.Exit(2)
	}

	if err := clientcmd.Run(context.Background(), cfg, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "symterm: %v\n", err)
		os.Exit(3)
	}
}
