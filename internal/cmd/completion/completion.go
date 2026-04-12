package completion

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

func Run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("symterm completion", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "show completion help")
	fs.BoolVar(&showHelp, "help", false, "show completion help")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if showHelp || fs.NArg() == 0 {
		_, _ = io.WriteString(stdout, Usage()+"\n")
		return nil
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("unexpected completion arguments: %v", fs.Args()[1:])
	}

	switch fs.Arg(0) {
	case "bash":
		_, _ = io.WriteString(stdout, BashScript())
		return nil
	default:
		return fmt.Errorf("unsupported shell %q", fs.Arg(0))
	}
}

func Usage() string {
	return strings.TrimSpace(`Usage: symterm completion bash

Generate shell completion scripts.
`)
}

func BashScript() string {
	return strings.TrimSpace(`
_symterm_has_double_dash() {
  local i
  for ((i=1; i<COMP_CWORD; i++)); do
    if [[ "${COMP_WORDS[i]}" == "--" ]]; then
      return 0
    fi
  done
  return 1
}

_symterm_complete_run() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev=""
  if (( COMP_CWORD > 0 )); then
    prev="${COMP_WORDS[COMP_CWORD-1]}"
  fi

  if _symterm_has_double_dash; then
    return 0
  fi

  case "$prev" in
    --project-id)
      return 0
      ;;
  esac

  COMPREPLY=( $(compgen -W "--project-id --confirm-reconcile --tmux-status -v --verbose --help --" -- "$cur") )
}

_symterm_complete_admin() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev=""
  if (( COMP_CWORD > 0 )); then
    prev="${COMP_WORDS[COMP_CWORD-1]}"
  fi

  case "$prev" in
    inspect|terminate|create|disable|issue|revoke|get|set)
      return 0
      ;;
  esac

  if (( COMP_CWORD == 2 )); then
    COMPREPLY=( $(compgen -W "daemon sessions users" -- "$cur") )
    return 0
  fi

  case "${COMP_WORDS[2]-}" in
    daemon)
      if (( COMP_CWORD == 3 )); then
        COMPREPLY=( $(compgen -W "info" -- "$cur") )
      fi
      return 0
      ;;
    sessions)
      if (( COMP_CWORD == 3 )); then
        COMPREPLY=( $(compgen -W "list inspect terminate" -- "$cur") )
      fi
      return 0
      ;;
    users)
      if (( COMP_CWORD == 3 )); then
        COMPREPLY=( $(compgen -W "list create disable token entrypoint" -- "$cur") )
        return 0
      fi
      case "${COMP_WORDS[3]-}" in
        token)
          if (( COMP_CWORD == 4 )); then
            COMPREPLY=( $(compgen -W "issue revoke" -- "$cur") )
          fi
          return 0
          ;;
        entrypoint)
          if (( COMP_CWORD == 4 )); then
            COMPREPLY=( $(compgen -W "get set" -- "$cur") )
          fi
          return 0
          ;;
      esac
      return 0
      ;;
  esac
}

_symterm_completion() {
  local cur
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"

  if (( COMP_CWORD == 1 )); then
    COMPREPLY=( $(compgen -W "run admin setup version completion" -- "$cur") )
    return 0
  fi

  case "${COMP_WORDS[1]-}" in
    run)
      _symterm_complete_run
      return 0
      ;;
    admin)
      _symterm_complete_admin
      return 0
      ;;
    setup)
      if (( COMP_CWORD == 2 )); then
        COMPREPLY=( $(compgen -W "--help" -- "$cur") )
      fi
      return 0
      ;;
    completion)
      if (( COMP_CWORD == 2 )); then
        COMPREPLY=( $(compgen -W "bash" -- "$cur") )
      fi
      return 0
      ;;
  esac
}

complete -F _symterm_completion -o bashdefault -o default symterm
`) + "\n"
}
