#!/usr/bin/env sh
set -eu

# Official symtermd install template.
# Fill the placeholder variables before delivery or export matching env vars.

SYMTERMD_DOWNLOAD_URL="${SYMTERMD_DOWNLOAD_URL:-__SYMTERMD_DOWNLOAD_URL__}"
SYMTERMD_VERSION="${SYMTERMD_VERSION:-__SYMTERMD_VERSION__}"
SYMTERMD_REMOTE_ENTRY="${SYMTERMD_REMOTE_ENTRY:-__SYMTERMD_REMOTE_ENTRY_JSON__}"
SYMTERMD_SSH_LISTEN_ADDR="${SYMTERMD_SSH_LISTEN_ADDR:-127.0.0.1:7000}"
SYMTERMD_ADMIN_WEB_ADDR="${SYMTERMD_ADMIN_WEB_ADDR:-127.0.0.1:6040}"
SYMTERMD_PROJECTS_ROOT="${SYMTERMD_PROJECTS_ROOT:-$HOME/.symterm}"
SYMTERMD_ALLOW_UNSAFE_NO_FUSE="${SYMTERMD_ALLOW_UNSAFE_NO_FUSE:-0}"
SYMTERMD_STATIC_TOKENS="${SYMTERMD_STATIC_TOKENS:-}"

INSTALL_ROOT="${INSTALL_ROOT:-$HOME/.local/lib/symterm}"
BIN_DIR="$INSTALL_ROOT/bin"
RUN_DIR="$INSTALL_ROOT/run"
CONFIG_DIR="$INSTALL_ROOT/config"
ENV_FILE="$CONFIG_DIR/symtermd.env"
BIN_PATH="$BIN_DIR/symtermd"
LOG_PATH="$RUN_DIR/symtermd.log"
SERVICE_NAME="${SERVICE_NAME:-symtermd}"
LINK_DIR="${LINK_DIR:-}"
USER_UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
USER_UNIT_PATH="$USER_UNIT_DIR/$SERVICE_NAME.service"
SYSTEM_UNIT_PATH="/etc/systemd/system/$SERVICE_NAME.service"
INITD_PATH="/etc/init.d/$SERVICE_NAME"
STATE_FILE="$RUN_DIR/install-mode"
SKIP_SETUP_WIZARD=0

maybe_reexec_with_bash() {
  if [ -n "${BASH_VERSION:-}" ]; then
    return 0
  fi
  if ! [ -t 0 ] || ! [ -t 1 ]; then
    return 0
  fi
  if ! command -v bash >/dev/null 2>&1; then
    return 0
  fi
  exec bash "$0" "$@"
}

usage() {
  cat <<EOF
Usage: ./tools/install-symtermd.sh [--skip-setup-wizard]

Installs or upgrades symtermd in place.

Options:
  --skip-setup-wizard  Use the current or pre-exported values without prompting.
  -h, --help           Show this help.
EOF
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --skip-setup-wizard)
        SKIP_SETUP_WIZARD=1
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

require_value() {
  value="$1"
  name="$2"
  case "$value" in
    ""|__*__)
      echo "missing required value for $name" >&2
      exit 1
      ;;
  esac
}

has_value() {
  case "$1" in
    ""|__*__)
      return 1
      ;;
    *)
      return 0
      ;;
  esac
}

quote_env_value() {
  printf "'%s'" "$(printf "%s" "$1" | sed "s/'/'\\\\''/g")"
}

local_source_path() {
  case "$1" in
    file://localhost/*)
      printf '%s\n' "${1#file://localhost}"
      ;;
    file:///*)
      printf '%s\n' "${1#file://}"
      ;;
    file://*)
      echo "unsupported file URI host in SYMTERMD_DOWNLOAD_URL: $1" >&2
      exit 1
      ;;
    *)
      printf '%s\n' "$1"
      ;;
  esac
}

download_symtermd() {
  mkdir -p "$BIN_DIR"
  tmp="$BIN_PATH.tmp"
  case "$SYMTERMD_DOWNLOAD_URL" in
    http://*|https://*)
      if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$SYMTERMD_DOWNLOAD_URL" -o "$tmp"
      elif command -v wget >/dev/null 2>&1; then
        wget -qO "$tmp" "$SYMTERMD_DOWNLOAD_URL"
      else
        echo "curl or wget is required to download symtermd" >&2
        exit 1
      fi
      ;;
    *)
      cp "$(local_source_path "$SYMTERMD_DOWNLOAD_URL")" "$tmp"
      ;;
  esac
  chmod 0755 "$tmp"
  mv "$tmp" "$BIN_PATH"
}

load_existing_env_defaults() {
  if [ ! -f "$ENV_FILE" ]; then
    return
  fi

  input_projects_root="$SYMTERMD_PROJECTS_ROOT"
  input_static_tokens="$SYMTERMD_STATIC_TOKENS"
  input_remote_entry="$SYMTERMD_REMOTE_ENTRY"
  input_ssh_listen_addr="$SYMTERMD_SSH_LISTEN_ADDR"
  input_admin_web_addr="$SYMTERMD_ADMIN_WEB_ADDR"
  input_allow_unsafe_no_fuse="$SYMTERMD_ALLOW_UNSAFE_NO_FUSE"

  # shellcheck disable=SC1090
  . "$ENV_FILE"

  if has_value "$input_projects_root"; then
    SYMTERMD_PROJECTS_ROOT="$input_projects_root"
  fi
  if has_value "$input_static_tokens"; then
    SYMTERMD_STATIC_TOKENS="$input_static_tokens"
  fi
  if has_value "$input_remote_entry"; then
    SYMTERMD_REMOTE_ENTRY="$input_remote_entry"
  fi
  if has_value "$input_ssh_listen_addr"; then
    SYMTERMD_SSH_LISTEN_ADDR="$input_ssh_listen_addr"
  fi
  if has_value "$input_admin_web_addr"; then
    SYMTERMD_ADMIN_WEB_ADDR="$input_admin_web_addr"
  fi
  if has_value "$input_allow_unsafe_no_fuse"; then
    SYMTERMD_ALLOW_UNSAFE_NO_FUSE="$input_allow_unsafe_no_fuse"
  fi
}

write_env_file() {
  mkdir -p "$CONFIG_DIR" "$RUN_DIR"
  cat >"$ENV_FILE" <<EOF
SYMTERMD_PROJECTS_ROOT=$(quote_env_value "$SYMTERMD_PROJECTS_ROOT")
SYMTERMD_STATIC_TOKENS=$(quote_env_value "$SYMTERMD_STATIC_TOKENS")
SYMTERMD_REMOTE_ENTRY=$(quote_env_value "$SYMTERMD_REMOTE_ENTRY")
SYMTERMD_SSH_LISTEN_ADDR=$(quote_env_value "$SYMTERMD_SSH_LISTEN_ADDR")
SYMTERMD_ADMIN_WEB_ADDR=$(quote_env_value "$SYMTERMD_ADMIN_WEB_ADDR")
SYMTERMD_ALLOW_UNSAFE_NO_FUSE=$(quote_env_value "$SYMTERMD_ALLOW_UNSAFE_NO_FUSE")
EOF
}

resolve_link_path() {
  if [ -n "$LINK_DIR" ]; then
    printf '%s/%s\n' "$LINK_DIR" "$SERVICE_NAME"
    return
  fi
  if [ "$(id -u)" -eq 0 ]; then
    printf '%s\n' "/usr/local/bin/$SERVICE_NAME"
    return
  fi
  printf '%s\n' "$HOME/.local/bin/$SERVICE_NAME"
}

link_symtermd_command() {
  link_path="$(resolve_link_path)"
  mkdir -p "$(dirname "$link_path")"
  ln -sfn "$BIN_PATH" "$link_path"
  printf '%s\n' "$link_path"
}

path_contains_dir() {
  dir="$1"
  case ":${PATH:-}:" in
    *":$dir:"*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

wait_for_pid_exit() {
  pid="$1"
  attempts=0
  while kill -0 "$pid" 2>/dev/null; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 50 ]; then
      echo "timed out waiting for pid $pid to exit" >&2
      exit 1
    fi
    sleep 0.1
  done
}

stop_user_systemd_if_running() {
  if ! command -v systemctl >/dev/null 2>&1; then
    return 1
  fi
  if ! systemctl --user is-active --quiet "$SERVICE_NAME.service" 2>/dev/null; then
    return 1
  fi
  systemctl --user stop "$SERVICE_NAME.service"
  return 0
}

stop_system_systemd_if_running() {
  if [ "$(id -u)" -ne 0 ]; then
    return 1
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    return 1
  fi
  if ! systemctl is-active --quiet "$SERVICE_NAME.service" 2>/dev/null; then
    return 1
  fi
  systemctl stop "$SERVICE_NAME.service"
  return 0
}

stop_initd_if_running() {
  if [ "$(id -u)" -ne 0 ]; then
    return 1
  fi
  if ! command -v service >/dev/null 2>&1; then
    return 1
  fi
  if [ ! -x "$INITD_PATH" ]; then
    return 1
  fi
  if ! service "$SERVICE_NAME" status >/dev/null 2>&1; then
    return 1
  fi
  service "$SERVICE_NAME" stop
  return 0
}

stop_background_if_running() {
  pid_file="$RUN_DIR/symtermd.pid"
  if [ ! -f "$pid_file" ]; then
    return 1
  fi
  pid="$(cat "$pid_file")"
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pid_file"
    return 1
  fi
  kill "$pid" 2>/dev/null || true
  wait_for_pid_exit "$pid"
  rm -f "$pid_file"
  return 0
}

stop_existing_service() {
  stopped_any=0

  if stop_user_systemd_if_running; then
    echo "stopped existing systemd --user service"
    stopped_any=1
  fi
  if stop_system_systemd_if_running; then
    echo "stopped existing system service"
    stopped_any=1
  fi
  if stop_initd_if_running; then
    echo "stopped existing init.d service"
    stopped_any=1
  fi
  if stop_background_if_running; then
    echo "stopped existing background service"
    stopped_any=1
  fi

  if [ "$stopped_any" -eq 0 ]; then
    echo "no running existing symtermd service detected"
  fi
}

install_user_systemd() {
  mkdir -p "$USER_UNIT_DIR"
  cat >"$USER_UNIT_PATH" <<EOF
[Unit]
Description=symterm daemon
After=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_PATH
Restart=always
RestartSec=2
TimeoutStopSec=10s
KillMode=mixed
StandardOutput=append:$LOG_PATH
StandardError=append:$LOG_PATH

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable --now "$SERVICE_NAME.service"
  echo "systemd-user" >"$STATE_FILE"
}

install_system_systemd() {
  cat >"$SYSTEM_UNIT_PATH" <<EOF
[Unit]
Description=symterm daemon
After=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_PATH
Restart=always
RestartSec=2
TimeoutStopSec=10s
KillMode=mixed
StandardOutput=append:$LOG_PATH
StandardError=append:$LOG_PATH

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now "$SERVICE_NAME.service"
  echo "systemd-system" >"$STATE_FILE"
}

install_initd() {
  cat >"$INITD_PATH" <<'EOF'
#!/usr/bin/env sh
### BEGIN INIT INFO
# Provides:          symtermd
# Required-Start:    $remote_fs $network
# Required-Stop:     $remote_fs $network
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
### END INIT INFO
set -eu
. "__ENV_FILE__"
case "${1:-}" in
  start)
    mkdir -p "__RUN_DIR__"
    if [ -f "__RUN_DIR__/symtermd.pid" ] && kill -0 "$(cat "__RUN_DIR__/symtermd.pid")" 2>/dev/null; then
      exit 0
    fi
    nohup "__BIN_PATH__" >>"__LOG_PATH__" 2>&1 &
    echo $! >"__RUN_DIR__/symtermd.pid"
    ;;
  stop)
    if [ -f "__RUN_DIR__/symtermd.pid" ]; then
      kill "$(cat "__RUN_DIR__/symtermd.pid")" 2>/dev/null || true
      rm -f "__RUN_DIR__/symtermd.pid"
    fi
    ;;
  restart)
    "$0" stop
    "$0" start
    ;;
  status)
    if [ -f "__RUN_DIR__/symtermd.pid" ] && kill -0 "$(cat "__RUN_DIR__/symtermd.pid")" 2>/dev/null; then
      exit 0
    fi
    exit 1
    ;;
  *)
    exit 2
    ;;
esac
EOF
  sed -i \
    -e "s|__ENV_FILE__|$ENV_FILE|g" \
    -e "s|__RUN_DIR__|$RUN_DIR|g" \
    -e "s|__BIN_PATH__|$BIN_PATH|g" \
    -e "s|__LOG_PATH__|$LOG_PATH|g" \
    "$INITD_PATH"
  chmod 0755 "$INITD_PATH"
  service "$SERVICE_NAME" restart
  echo "initd" >"$STATE_FILE"
}

install_background() {
  mkdir -p "$RUN_DIR"
  if [ -f "$RUN_DIR/symtermd.pid" ] && kill -0 "$(cat "$RUN_DIR/symtermd.pid")" 2>/dev/null; then
    kill "$(cat "$RUN_DIR/symtermd.pid")" 2>/dev/null || true
  fi
  nohup sh -c '. "$1"; exec "$2"' sh "$ENV_FILE" "$BIN_PATH" >>"$LOG_PATH" 2>&1 &
  echo $! >"$RUN_DIR/symtermd.pid"
  echo "background" >"$STATE_FILE"
}

is_interactive_terminal() {
  [ -t 0 ] && [ -t 1 ]
}

prompt_default() {
  label="$1"
  default_value="$2"
  if [ -n "${BASH_VERSION:-}" ]; then
    if [ -n "$default_value" ]; then
      read -e -r -p "$label [$default_value]: " -i "$default_value" value || true
    else
      read -e -r -p "$label: " value || true
    fi
  else
    if [ -n "$default_value" ]; then
      printf "%s [%s]: " "$label" "$default_value" >&2
    else
      printf "%s: " "$label" >&2
    fi
    IFS= read -r value || true
  fi
  if [ -z "$value" ]; then
    value="$default_value"
  fi
  printf '%s\n' "$value"
}

prompt_required_default() {
  label="$1"
  default_value="$2"
  while :; do
    value="$(prompt_default "$label" "$default_value")"
    if [ -n "$(printf '%s' "$value" | tr -d '[:space:]')" ]; then
      printf '%s\n' "$value"
      return 0
    fi
    echo "Value is required." >&2
  done
}

bool_default_label() {
  case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on)
      printf 'yes'
      ;;
    *)
      printf 'no'
      ;;
  esac
}

prompt_yes_no_default() {
  label="$1"
  default_value="$(bool_default_label "$2")"
  while :; do
    value="$(prompt_default "$label (yes/no)" "$default_value")"
    case "$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')" in
      y|yes)
        printf '1\n'
        return 0
        ;;
      n|no)
        printf '0\n'
        return 0
        ;;
      *)
        echo "Enter yes or no." >&2
        ;;
    esac
  done
}

prompt_remote_entry_json() {
  default_value="$1"
  while :; do
    value="$(prompt_required_default "Remote entry JSON argv" "$default_value")"
    case "$value" in
      \[*\])
        printf '%s\n' "$value"
        return 0
        ;;
      *)
        echo "Enter a JSON argv array, for example: [\"/usr/bin/env\",\"bash\",\"-lc\"]" >&2
        ;;
    esac
  done
}

has_setup_defaults() {
  has_value "$SYMTERMD_PROJECTS_ROOT" && return 0
  has_value "$SYMTERMD_REMOTE_ENTRY" && return 0
  has_value "$SYMTERMD_SSH_LISTEN_ADDR" && return 0
  has_value "$SYMTERMD_ADMIN_WEB_ADDR" && return 0
  has_value "$SYMTERMD_ALLOW_UNSAFE_NO_FUSE" && return 0
  return 1
}

run_setup_wizard() {
  if [ "$SKIP_SETUP_WIZARD" -eq 1 ]; then
    return
  fi
  if ! is_interactive_terminal; then
    echo "stdin/stdout is not a terminal; skipping setup wizard"
    return
  fi

  echo "symtermd setup wizard"
  echo "Configure the daemon environment before install."
  if has_setup_defaults; then
    echo "Current values are shown as defaults. Press Enter to keep them."
  fi
  echo

  SYMTERMD_PROJECTS_ROOT="$(prompt_required_default "Projects root" "$SYMTERMD_PROJECTS_ROOT")"
  SYMTERMD_REMOTE_ENTRY="$(prompt_remote_entry_json "$SYMTERMD_REMOTE_ENTRY")"
  SYMTERMD_SSH_LISTEN_ADDR="$(prompt_required_default "SSH listen address" "$SYMTERMD_SSH_LISTEN_ADDR")"
  SYMTERMD_ADMIN_WEB_ADDR="$(prompt_required_default "Admin web address" "$SYMTERMD_ADMIN_WEB_ADDR")"
  SYMTERMD_ALLOW_UNSAFE_NO_FUSE="$(prompt_yes_no_default "Allow unsafe no FUSE for local testing" "$SYMTERMD_ALLOW_UNSAFE_NO_FUSE")"
  echo
}

main() {
  maybe_reexec_with_bash "$@"
  parse_args "$@"
  load_existing_env_defaults
  run_setup_wizard

  require_value "$SYMTERMD_DOWNLOAD_URL" "SYMTERMD_DOWNLOAD_URL"
  require_value "$SYMTERMD_VERSION" "SYMTERMD_VERSION"
  require_value "$SYMTERMD_REMOTE_ENTRY" "SYMTERMD_REMOTE_ENTRY"

  case "$SYMTERMD_REMOTE_ENTRY" in
    \[*\])
      ;;
    *)
      echo "SYMTERMD_REMOTE_ENTRY should be a JSON argv array, for example:" >&2
      echo "  export SYMTERMD_REMOTE_ENTRY='[\"/usr/bin/env\",\"bash\",\"-lc\"]'" >&2
      exit 1
      ;;
  esac

  stop_existing_service
  download_symtermd
  write_env_file
  link_path="$(link_symtermd_command)"

  if command -v systemctl >/dev/null 2>&1 && systemctl --user --version >/dev/null 2>&1; then
    install_user_systemd
  elif command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    install_system_systemd
  elif command -v service >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    install_initd
  else
    install_background
  fi

  echo "symtermd $SYMTERMD_VERSION installed"
  echo "binary path: $BIN_PATH"
  echo "command link: $link_path"
  echo "service mode: $(cat "$STATE_FILE")"
  if ! path_contains_dir "$(dirname "$link_path")"; then
    echo "warning: $(dirname "$link_path") is not in PATH for this shell"
  fi
}

main "$@"
