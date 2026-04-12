package main

type commandRoute string

const (
	routeDefault         commandRoute = "default"
	routeRun             commandRoute = "run"
	routeCompletion      commandRoute = "completion"
	routeSetup           commandRoute = "setup"
	routeAdmin           commandRoute = "admin"
	routeVersion         commandRoute = "version"
	routeAuthorityBroker commandRoute = "internal-authority-broker"
	routeHelp            commandRoute = "help"
)

func classifyCommandRoute(args []string) commandRoute {
	if len(args) == 1 {
		switch args[0] {
		case "-h", "--help":
			return routeHelp
		}
	}
	if len(args) == 0 {
		return routeDefault
	}
	switch args[0] {
	case "run":
		return routeRun
	case "completion":
		return routeCompletion
	case "setup":
		return routeSetup
	case "admin":
		return routeAdmin
	case "version":
		return routeVersion
	case "internal-authority-broker":
		return routeAuthorityBroker
	default:
		return routeDefault
	}
}

func rootUsage() string {
	return `Usage:
  symterm <remote-argv...>
  symterm run [local-options] -- <remote-argv...>
  symterm completion bash
  symterm admin ...
  symterm setup
  symterm version

Commands:
  run
        explicit local-control entrypoint for project flags and command disambiguation
  completion
        print shell completion scripts
  admin
        local daemon admin client
  setup
        interactive local client configuration wizard
  version
        print build version

Run "symterm run --help" for local run options.
`
}
