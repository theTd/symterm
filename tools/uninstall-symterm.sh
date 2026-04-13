#!/usr/bin/env sh
set -eu

# Official symterm uninstaller for Unix-like hosts.
# It removes symtermd by default and prompts about client/data removal
# unless those choices are made explicitly with flags.

INSTALL_ROOT="${INSTALL_ROOT:-$HOME/.local/lib/symterm}"
BIN_DIR="$INSTALL_ROOT/bin"
RUN_DIR="$INSTALL_ROOT/run"
CONFIG_DIR="$INSTALL_ROOT/config"
ENV_FILE="$CONFIG_DIR/symtermd.env"
CLIENT_BIN_PATH="$BIN_DIR/symterm"
DAEMON_BIN_PATH="$BIN_DIR/symtermd"
LOG_PATH="$RUN_DIR/symtermd.log"
SERVICE_NAME="${SERVICE_NAME:-symtermd}"
LINK_DIR="${LINK_DIR:-}"
BASH_COMPLETION_DIR="${BASH_COMPLETION_DIR:-}"
USER_UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
USER_UNIT_PATH="$USER_UNIT_DIR/$SERVICE_NAME.service"
SYSTEM_UNIT_PATH="/etc/systemd/system/$SERVICE_NAME.service"
INITD_PATH="/etc/init.d/$SERVICE_NAME"
STATE_FILE="$RUN_DIR/install-mode"
PID_FILE="$RUN_DIR/symtermd.pid"
DEFAULT_PROJECTS_ROOT="${SYMTERMD_PROJECTS_ROOT:-$HOME/.symterm}"

REMOVE_DAEMON=1
REMOVE_CLIENT=0
PURGE_DATA=0
PROJECTS_ROOT_TO_PURGE=""
CLIENT_CHOICE_SET=0
PURGE_CHOICE_SET=0
ASSUME_YES=0

has_prompt_terminal() {
  [ -r /dev/tty ] && [ -w /dev/tty ] || return 1
  : </dev/tty >/dev/tty 2>/dev/null
}

usage() {
  cat <<EOF
Usage: ./tools/uninstall-symterm.sh [--yes] [--daemon-only] [--client-only] [--all] [--purge-data] [--keep-data] [--remove-client] [--keep-client]

Uninstalls symtermd by default. Unless you pass explicit flags, the script asks
whether to also remove the symterm client and whether to purge daemon user data.
Unless you pass --yes, the script also asks for final confirmation before making
changes.

Options:
  --yes            Skip the final confirmation prompt.
  --daemon-only    Remove only symtermd and keep the client.
  --client-only    Remove only the symterm client binary and command link.
  --all            Remove both symterm and symtermd.
  --remove-client  Explicitly remove the symterm client.
  --keep-client    Explicitly keep the symterm client.
  --purge-data     Explicitly delete the daemon projects root.
  --keep-data      Explicitly preserve the daemon projects root.
  -h, --help       Show this help.
EOF
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -y|--yes)
        ASSUME_YES=1
        ;;
      --daemon-only)
        REMOVE_DAEMON=1
        REMOVE_CLIENT=0
        CLIENT_CHOICE_SET=1
        ;;
      --client-only)
        REMOVE_DAEMON=0
        REMOVE_CLIENT=1
        CLIENT_CHOICE_SET=1
        ;;
      --all)
        REMOVE_DAEMON=1
        REMOVE_CLIENT=1
        CLIENT_CHOICE_SET=1
        ;;
      --remove-client)
        REMOVE_CLIENT=1
        CLIENT_CHOICE_SET=1
        ;;
      --keep-client)
        REMOVE_CLIENT=0
        CLIENT_CHOICE_SET=1
        ;;
      --purge-data)
        PURGE_DATA=1
        PURGE_CHOICE_SET=1
        ;;
      --keep-data)
        PURGE_DATA=0
        PURGE_CHOICE_SET=1
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "unknown argument: $1" >&2
        usage >&2
        exit 1
        ;;
    esac
    shift
  done
}

is_interactive_terminal() {
  has_prompt_terminal
}

prompt_yes_no_default() {
  label="$1"
  default_answer="$2"

  if ! has_prompt_terminal; then
    echo "no interactive terminal is available for prompts" >&2
    exit 1
  fi

  case "$default_answer" in
    y|Y)
      prompt="$label [Y/n]: "
      ;;
    *)
      prompt="$label [y/N]: "
      ;;
  esac

  while true; do
    printf '%s' "$prompt" >/dev/tty
    IFS= read -r answer </dev/tty || true
    case "$answer" in
      '')
        case "$default_answer" in
          y|Y)
            return 0
            ;;
          *)
            return 1
            ;;
        esac
        ;;
      y|Y|yes|YES|Yes)
        return 0
        ;;
      n|N|no|NO|No)
        return 1
        ;;
      *)
        echo "please answer y or n" >&2
        ;;
    esac
  done
}

apply_interactive_choices() {
  if is_interactive_terminal; then
    if [ "$REMOVE_DAEMON" -eq 1 ] && [ "$CLIENT_CHOICE_SET" -eq 0 ]; then
      if prompt_yes_no_default "Also uninstall the symterm client" "n"; then
        REMOVE_CLIENT=1
      else
        REMOVE_CLIENT=0
      fi
      CLIENT_CHOICE_SET=1
    fi
    if [ "$REMOVE_DAEMON" -eq 1 ] && [ "$PURGE_CHOICE_SET" -eq 0 ]; then
      if prompt_yes_no_default "Delete daemon user data at $(read_projects_root)" "n"; then
        PURGE_DATA=1
      else
        PURGE_DATA=0
      fi
      PURGE_CHOICE_SET=1
    fi
    return 0
  fi

  if [ "$REMOVE_DAEMON" -eq 1 ] && [ "$CLIENT_CHOICE_SET" -eq 0 ]; then
    echo "no interactive terminal is available; keeping the symterm client (use --remove-client to override)"
  fi
  if [ "$REMOVE_DAEMON" -eq 1 ] && [ "$PURGE_CHOICE_SET" -eq 0 ]; then
    echo "no interactive terminal is available; preserving daemon user data (use --purge-data to override)"
  fi
}

validate_args() {
  if [ "$REMOVE_DAEMON" -eq 0 ] && [ "$PURGE_DATA" -eq 1 ]; then
    echo "--purge-data requires daemon removal" >&2
    exit 1
  fi
}

print_plan() {
  if [ "$REMOVE_DAEMON" -eq 1 ]; then
    echo "daemon removal: yes"
  else
    echo "daemon removal: no"
  fi
  if [ "$REMOVE_CLIENT" -eq 1 ]; then
    echo "client removal: yes"
  else
    echo "client removal: no"
  fi
  if [ "$PURGE_DATA" -eq 1 ]; then
    echo "daemon data purge: yes ($PROJECTS_ROOT_TO_PURGE)"
  else
    echo "daemon data purge: no"
  fi
}

confirm_uninstall() {
  if [ "$ASSUME_YES" -eq 1 ]; then
    return 0
  fi

  if ! is_interactive_terminal; then
    echo "final confirmation required in non-interactive mode; rerun with --yes to proceed" >&2
    exit 1
  fi

  echo "planned uninstall actions:"
  print_plan
  if ! prompt_yes_no_default "Proceed with uninstall" "n"; then
    echo "uninstall cancelled"
    exit 1
  fi
}

resolve_link_path() {
  link_name="$1"
  if [ -n "$LINK_DIR" ]; then
    printf '%s/%s\n' "$LINK_DIR" "$link_name"
  elif [ "$(id -u)" -eq 0 ]; then
    printf '%s\n' "/usr/local/bin/$link_name"
  else
    printf '%s\n' "$HOME/.local/bin/$link_name"
  fi
}

resolve_bash_completion_dir() {
  if [ -n "$BASH_COMPLETION_DIR" ]; then
    printf '%s\n' "$BASH_COMPLETION_DIR"
  elif [ "$(id -u)" -eq 0 ]; then
    printf '%s\n' "/usr/local/share/bash-completion/completions"
  else
    printf '%s\n' "${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions"
  fi
}

remove_path_if_present() {
  path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    rm -rf "$path"
    printf 'removed %s\n' "$path"
  fi
}

prune_dir_if_empty() {
  path="$1"
  if [ -d "$path" ]; then
    rmdir "$path" 2>/dev/null || true
  fi
}

reload_user_systemd() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user daemon-reload >/dev/null 2>&1 || true
    systemctl --user reset-failed "$SERVICE_NAME.service" >/dev/null 2>&1 || true
  fi
}

reload_system_systemd() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl reset-failed "$SERVICE_NAME.service" >/dev/null 2>&1 || true
  fi
}

stop_user_systemd_service() {
  command -v systemctl >/dev/null 2>&1 || return 0
  systemctl --user disable --now "$SERVICE_NAME.service" >/dev/null 2>&1 || \
    systemctl --user stop "$SERVICE_NAME.service" >/dev/null 2>&1 || true
}

stop_system_systemd_service() {
  command -v systemctl >/dev/null 2>&1 || return 0
  systemctl disable --now "$SERVICE_NAME.service" >/dev/null 2>&1 || \
    systemctl stop "$SERVICE_NAME.service" >/dev/null 2>&1 || true
}

stop_initd_service() {
  command -v service >/dev/null 2>&1 || return 0
  [ -e "$INITD_PATH" ] || return 0
  service "$SERVICE_NAME" stop >/dev/null 2>&1 || true
}

stop_background_service() {
  [ -f "$PID_FILE" ] || return 0
  pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  case "$pid" in
    ''|*[!0-9]*)
      remove_path_if_present "$PID_FILE"
      return 0
      ;;
  esac
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
  fi
  remove_path_if_present "$PID_FILE"
}

stop_daemon_services() {
  stop_user_systemd_service
  stop_system_systemd_service
  stop_initd_service
  stop_background_service
}

read_projects_root() {
  if [ -f "$ENV_FILE" ]; then
    line="$(sed -n "s/^SYMTERMD_PROJECTS_ROOT=//p" "$ENV_FILE" | head -n 1)"
    if [ -n "$line" ]; then
      case "$line" in
        \'*\')
          line="${line#\'}"
          line="${line%\'}"
          ;;
      esac
      printf '%s\n' "$line"
      return 0
    fi
  fi
  printf '%s\n' "$DEFAULT_PROJECTS_ROOT"
}

remove_daemon() {
  daemon_link_path="$(resolve_link_path "symtermd")"
  stop_daemon_services

  remove_path_if_present "$USER_UNIT_PATH"
  reload_user_systemd

  remove_path_if_present "$SYSTEM_UNIT_PATH"
  reload_system_systemd

  remove_path_if_present "$INITD_PATH"
  remove_path_if_present "$daemon_link_path"
  remove_path_if_present "$DAEMON_BIN_PATH"
  remove_path_if_present "$ENV_FILE"
  remove_path_if_present "$LOG_PATH"
  remove_path_if_present "$STATE_FILE"
  remove_path_if_present "$PID_FILE"
}

remove_client() {
  client_link_path="$(resolve_link_path "symterm")"
  bash_completion_path="$(resolve_bash_completion_dir)/symterm"
  remove_path_if_present "$client_link_path"
  remove_path_if_present "$CLIENT_BIN_PATH"
  remove_path_if_present "$bash_completion_path"
}

purge_daemon_data() {
  projects_root="$PROJECTS_ROOT_TO_PURGE"
  if [ -z "$projects_root" ]; then
    projects_root="$(read_projects_root)"
  fi
  if [ -n "$projects_root" ] && [ "$projects_root" != "/" ]; then
    remove_path_if_present "$projects_root"
  fi
}

prune_install_tree() {
  prune_dir_if_empty "$CONFIG_DIR"
  prune_dir_if_empty "$RUN_DIR"
  prune_dir_if_empty "$BIN_DIR"
  prune_dir_if_empty "$INSTALL_ROOT"
  prune_dir_if_empty "$(resolve_bash_completion_dir)"
}

print_summary() {
  echo "symterm uninstall complete"
  if [ "$REMOVE_DAEMON" -eq 1 ]; then
    echo "daemon removed: yes"
  else
    echo "daemon removed: no"
  fi
  if [ "$REMOVE_CLIENT" -eq 1 ]; then
    echo "client removed: yes"
  else
    echo "client removed: no"
  fi
  if [ "$PURGE_DATA" -eq 1 ]; then
    echo "daemon data removed: yes"
  else
    echo "daemon data preserved: $(read_projects_root)"
  fi
}

main() {
  parse_args "$@"
  apply_interactive_choices
  validate_args

  if [ "$PURGE_DATA" -eq 1 ]; then
    PROJECTS_ROOT_TO_PURGE="$(read_projects_root)"
  fi

  confirm_uninstall

  if [ "$REMOVE_DAEMON" -eq 1 ]; then
    remove_daemon
  fi
  if [ "$REMOVE_CLIENT" -eq 1 ]; then
    remove_client
  fi
  if [ "$PURGE_DATA" -eq 1 ]; then
    purge_daemon_data
  fi

  prune_install_tree
  print_summary
}

main "$@"
