#!/usr/bin/env sh
set -eu

# Official symterm installer for Unix-like hosts.
# It always installs the client first, then optionally installs the daemon.

SYMTERM_REPO="${SYMTERM_REPO:-${SYMTERM_GITHUB_REPO_URL:-${SYMTERM_GITHUB_REPO:-https://github.com/theTd/symterm/}}}"
SYMTERM_VERSION="${SYMTERM_VERSION:-${SYMTERMD_VERSION:-}}"
SYMTERM_DOWNLOAD_URL="${SYMTERM_DOWNLOAD_URL:-}"
SYMTERMD_DOWNLOAD_URL="${SYMTERMD_DOWNLOAD_URL:-}"
SYMTERM_INSTALL_DAEMON="${SYMTERM_INSTALL_DAEMON:-auto}"
SYMTERMD_REMOTE_ENTRY="${SYMTERMD_REMOTE_ENTRY:-[\"bash\"]}"
SYMTERMD_SSH_LISTEN_ADDR="${SYMTERMD_SSH_LISTEN_ADDR:-127.0.0.1:7000}"
SYMTERMD_ADMIN_WEB_ADDR="${SYMTERMD_ADMIN_WEB_ADDR:-127.0.0.1:6040}"
SYMTERMD_PROJECTS_ROOT="${SYMTERMD_PROJECTS_ROOT:-$HOME/.symterm}"
SYMTERMD_ALLOW_UNSAFE_NO_FUSE="${SYMTERMD_ALLOW_UNSAFE_NO_FUSE:-0}"
SYMTERMD_STATIC_TOKENS="${SYMTERMD_STATIC_TOKENS:-}"

INSTALL_ROOT="${INSTALL_ROOT:-$HOME/.local/lib/symterm}"
SERVICE_NAME="${SERVICE_NAME:-symtermd}"
LINK_DIR="${LINK_DIR:-}"
USER_UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
SYSTEM_UNIT_PATH="/etc/systemd/system/$SERVICE_NAME.service"
INITD_PATH="/etc/init.d/$SERVICE_NAME"

expand_path() {
  case "$1" in
    "~")
      printf '%s\n' "$HOME"
      ;;
    "~/"*)
      printf '%s/%s\n' "$HOME" "${1#~/}"
      ;;
    *)
      printf '%s\n' "$1"
      ;;
  esac
}

INSTALL_ROOT="$(expand_path "$INSTALL_ROOT")"
LINK_DIR="$(expand_path "$LINK_DIR")"
USER_UNIT_DIR="$(expand_path "$USER_UNIT_DIR")"

BIN_DIR="$INSTALL_ROOT/bin"
RUN_DIR="$INSTALL_ROOT/run"
CONFIG_DIR="$INSTALL_ROOT/config"
ENV_FILE="$CONFIG_DIR/symtermd.env"
CLIENT_BIN_PATH="$BIN_DIR/symterm"
DAEMON_BIN_PATH="$BIN_DIR/symtermd"
LOG_PATH="$RUN_DIR/symtermd.log"
USER_UNIT_PATH="$USER_UNIT_DIR/$SERVICE_NAME.service"
STATE_FILE="$RUN_DIR/install-mode"

SKIP_SETUP_WIZARD=0
REPO_SLUG=""
REPO_URL=""
RELEASE_OS=""
RELEASE_ARCH=""
RESOLVED_VERSION=""
ASSET_VERSION=""
DAEMON_SUPPORTED=0
DAEMON_REQUESTED=0
DAEMON_INSTALLED=0
CLIENT_LINK_PATH=""
DAEMON_LINK_PATH=""

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
Usage: ./tools/install-symterm.sh [--skip-setup-wizard] [--install-daemon] [--skip-daemon]

Installs or upgrades symterm, then optionally installs symtermd.

Options:
  --skip-setup-wizard  Use current or exported daemon values without prompting.
  --install-daemon     Force daemon install without prompting.
  --skip-daemon        Install only the client.
  -h, --help           Show this help.
EOF
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --skip-setup-wizard)
        SKIP_SETUP_WIZARD=1
        ;;
      --install-daemon)
        SYMTERM_INSTALL_DAEMON="yes"
        ;;
      --skip-daemon)
        SYMTERM_INSTALL_DAEMON="no"
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

require_value() {
  if ! has_value "$1"; then
    echo "missing required value for $2" >&2
    exit 1
  fi
}

lowercase() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

quote_env_value() {
  printf "'%s'" "$(printf "%s" "$1" | sed "s/'/'\\\\''/g")"
}

source_basename() {
  source_basename_url="$1"
  source_basename_url="${source_basename_url%%\?*}"
  source_basename_url="${source_basename_url%%\#*}"
  printf '%s\n' "${source_basename_url##*/}"
}

local_source_path() {
  local_source_input="$1"
  case "$local_source_input" in
    file://localhost/*)
      printf '%s\n' "${local_source_input#file://localhost}"
      ;;
    file:///*)
      printf '%s\n' "${local_source_input#file://}"
      ;;
    file://*)
      echo "unsupported file URI host in download source: $local_source_input" >&2
      exit 1
      ;;
    *)
      printf '%s\n' "$local_source_input"
      ;;
  esac
}

normalize_repo_slug() {
  input="$(printf '%s' "$1" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
  input="${input%/}"
  input="${input%.git}"
  case "$input" in
    https://github.com/*|http://github.com/*)
      slug="${input#*://github.com/}"
      ;;
    git@github.com:*)
      slug="${input#git@github.com:}"
      ;;
    */*)
      slug="$input"
      ;;
    *)
      echo "unsupported GitHub repository value: $input" >&2
      exit 1
      ;;
  esac
  owner="${slug%%/*}"
  repo_path="${slug#*/}"
  repo="${repo_path%%/*}"
  if [ -z "$owner" ] || [ -z "$repo" ]; then
    echo "failed to parse GitHub repository from: $input" >&2
    exit 1
  fi
  REPO_SLUG="$owner/$repo"
  REPO_URL="https://github.com/$REPO_SLUG/"
}

github_api_get() {
  github_api_url="$1"
  if command -v curl >/dev/null 2>&1; then
    if [ -n "${GITHUB_TOKEN:-}" ]; then
      curl -fsSL -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $GITHUB_TOKEN" -H "User-Agent: symterm-install-script" "$github_api_url"
    else
      curl -fsSL -H "Accept: application/vnd.github+json" -H "User-Agent: symterm-install-script" "$github_api_url"
    fi
    return 0
  fi
  if command -v wget >/dev/null 2>&1; then
    if [ -n "${GITHUB_TOKEN:-}" ]; then
      wget -qO- --header="Accept: application/vnd.github+json" --header="Authorization: Bearer $GITHUB_TOKEN" --header="User-Agent: symterm-install-script" "$github_api_url"
    else
      wget -qO- --header="Accept: application/vnd.github+json" --header="User-Agent: symterm-install-script" "$github_api_url"
    fi
    return 0
  fi
  echo "curl or wget is required to query GitHub releases" >&2
  exit 1
}

download_to_file() {
  download_source_url="$1"
  download_destination_path="$2"
  case "$download_source_url" in
    http://*|https://*)
      if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$download_source_url" -o "$download_destination_path"
      elif command -v wget >/dev/null 2>&1; then
        wget -qO "$download_destination_path" "$download_source_url"
      else
        echo "curl or wget is required to download release assets" >&2
        exit 1
      fi
      ;;
    *)
      cp "$(local_source_path "$download_source_url")" "$download_destination_path"
      ;;
  esac
}

detect_platform() {
  case "$(uname -s)" in
    Linux)
      RELEASE_OS="linux"
      DAEMON_SUPPORTED=1
      ;;
    Darwin)
      RELEASE_OS="darwin"
      DAEMON_SUPPORTED=0
      ;;
    *)
      echo "unsupported operating system: $(uname -s)" >&2
      exit 1
      ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64)
      RELEASE_ARCH="amd64"
      ;;
    aarch64|arm64)
      RELEASE_ARCH="arm64"
      ;;
    *)
      echo "unsupported CPU architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

resolve_release_version() {
  if has_value "$SYMTERM_VERSION"; then
    RESOLVED_VERSION="$SYMTERM_VERSION"
  else
    release_json="$(github_api_get "https://api.github.com/repos/$REPO_SLUG/releases/latest")"
    RESOLVED_VERSION="$(printf '%s\n' "$release_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  fi
  if [ -z "$RESOLVED_VERSION" ]; then
    echo "failed to resolve the latest GitHub release tag for $REPO_URL" >&2
    exit 1
  fi
  ASSET_VERSION="${RESOLVED_VERSION#v}"
}

release_asset_name() {
  printf 'symterm_%s_%s_%s_%s.tar.gz\n' "$1" "$ASSET_VERSION" "$RELEASE_OS" "$RELEASE_ARCH"
}

release_asset_url() {
  asset_name="$(release_asset_name "$1")"
  printf 'https://github.com/%s/releases/download/%s/%s\n' "$REPO_SLUG" "$RESOLVED_VERSION" "$asset_name"
}

install_binary_from_source() {
  install_source_url="$1"
  install_expected_binary="$2"
  install_destination_path="$3"

  mkdir -p "$BIN_DIR"
  install_tmp_dir="$(mktemp -d)"
  install_asset_path="$install_tmp_dir/asset"
  download_to_file "$install_source_url" "$install_asset_path"
  case "$(source_basename "$install_source_url")" in
    *.tar.gz|*.tgz)
      if ! command -v tar >/dev/null 2>&1; then
        echo "tar is required to extract release assets" >&2
        exit 1
      fi
      install_extract_dir="$install_tmp_dir/extracted"
      mkdir -p "$install_extract_dir"
      tar -xzf "$install_asset_path" -C "$install_extract_dir"
      install_extracted_path="$(find "$install_extract_dir" -type f -name "$install_expected_binary" | head -n 1)"
      if [ -z "$install_extracted_path" ]; then
        echo "release asset did not contain $install_expected_binary" >&2
        exit 1
      fi
      cp "$install_extracted_path" "$install_destination_path.tmp"
      ;;
    *)
      cp "$install_asset_path" "$install_destination_path.tmp"
      ;;
  esac
  chmod 0755 "$install_destination_path.tmp"
  mv "$install_destination_path.tmp" "$install_destination_path"
  rm -rf "$install_tmp_dir"
}

resolve_link_path() {
  resolve_link_name="$1"
  if [ -n "$LINK_DIR" ]; then
    printf '%s/%s\n' "$LINK_DIR" "$resolve_link_name"
  elif [ "$(id -u)" -eq 0 ]; then
    printf '%s\n' "/usr/local/bin/$resolve_link_name"
  else
    printf '%s\n' "$HOME/.local/bin/$resolve_link_name"
  fi
}

link_command() {
  link_target_path="$1"
  link_name="$2"
  link_path="$(resolve_link_path "$link_name")"
  if [ ! -f "$link_target_path" ]; then
    echo "refusing to create command link for missing target: $link_target_path" >&2
    exit 1
  fi
  mkdir -p "$(dirname "$link_path")"
  ln -sfn "$link_target_path" "$link_path"
  if [ ! -e "$link_path" ]; then
    echo "created command link is broken: $link_path -> $link_target_path" >&2
    exit 1
  fi
  printf '%s\n' "$link_path"
}

path_contains_dir() {
  case ":${PATH:-}:" in
    *":$1:"*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
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
  while :; do
    value="$(prompt_default "$1" "$2")"
    if [ -n "$(printf '%s' "$value" | tr -d '[:space:]')" ]; then
      printf '%s\n' "$value"
      return 0
    fi
    echo "Value is required." >&2
  done
}

prompt_yes_no_default() {
  label="$1"
  default_choice="$(lowercase "$2")"
  case "$default_choice" in
    1|yes|true|on)
      default_value="yes"
      ;;
    *)
      default_value="no"
      ;;
  esac
  while :; do
    value="$(prompt_default "$label (yes/no)" "$default_value")"
    case "$(lowercase "$value")" in
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
  while :; do
    value="$(prompt_required_default "Remote entry JSON argv" "$1")"
    case "$value" in
      \[*\])
        printf '%s\n' "$value"
        return 0
        ;;
      *)
        echo "Enter a JSON argv array, for example: [\"bash\"]" >&2
        ;;
    esac
  done
}

parse_install_daemon_mode() {
  case "$(lowercase "$SYMTERM_INSTALL_DAEMON")" in
    ""|auto)
      printf 'auto\n'
      ;;
    1|true|yes|on)
      printf 'yes\n'
      ;;
    0|false|no|off)
      printf 'no\n'
      ;;
    *)
      echo "invalid value for SYMTERM_INSTALL_DAEMON: $SYMTERM_INSTALL_DAEMON" >&2
      exit 1
      ;;
  esac
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
  if has_value "$input_projects_root"; then SYMTERMD_PROJECTS_ROOT="$input_projects_root"; fi
  if has_value "$input_static_tokens"; then SYMTERMD_STATIC_TOKENS="$input_static_tokens"; fi
  if has_value "$input_remote_entry"; then SYMTERMD_REMOTE_ENTRY="$input_remote_entry"; fi
  if has_value "$input_ssh_listen_addr"; then SYMTERMD_SSH_LISTEN_ADDR="$input_ssh_listen_addr"; fi
  if has_value "$input_admin_web_addr"; then SYMTERMD_ADMIN_WEB_ADDR="$input_admin_web_addr"; fi
  if has_value "$input_allow_unsafe_no_fuse"; then SYMTERMD_ALLOW_UNSAFE_NO_FUSE="$input_allow_unsafe_no_fuse"; fi
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
  echo
  SYMTERMD_PROJECTS_ROOT="$(prompt_required_default "Projects root" "$SYMTERMD_PROJECTS_ROOT")"
  SYMTERMD_REMOTE_ENTRY="$(prompt_remote_entry_json "$SYMTERMD_REMOTE_ENTRY")"
  SYMTERMD_SSH_LISTEN_ADDR="$(prompt_required_default "SSH listen address" "$SYMTERMD_SSH_LISTEN_ADDR")"
  SYMTERMD_ADMIN_WEB_ADDR="$(prompt_required_default "Admin web address" "$SYMTERMD_ADMIN_WEB_ADDR")"
  SYMTERMD_ALLOW_UNSAFE_NO_FUSE="$(prompt_yes_no_default "Allow unsafe no FUSE for local testing" "$SYMTERMD_ALLOW_UNSAFE_NO_FUSE")"
  echo
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
  command -v systemctl >/dev/null 2>&1 || return 1
  systemctl --user is-active --quiet "$SERVICE_NAME.service" 2>/dev/null || return 1
  systemctl --user stop "$SERVICE_NAME.service"
}

stop_system_systemd_if_running() {
  [ "$(id -u)" -eq 0 ] || return 1
  command -v systemctl >/dev/null 2>&1 || return 1
  systemctl is-active --quiet "$SERVICE_NAME.service" 2>/dev/null || return 1
  systemctl stop "$SERVICE_NAME.service"
}

stop_initd_if_running() {
  [ "$(id -u)" -eq 0 ] || return 1
  command -v service >/dev/null 2>&1 || return 1
  [ -x "$INITD_PATH" ] || return 1
  service "$SERVICE_NAME" status >/dev/null 2>&1 || return 1
  service "$SERVICE_NAME" stop
}

stop_background_if_running() {
  pid_file="$RUN_DIR/symtermd.pid"
  [ -f "$pid_file" ] || return 1
  pid="$(cat "$pid_file")"
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pid_file"
    return 1
  fi
  kill "$pid" 2>/dev/null || true
  wait_for_pid_exit "$pid"
  rm -f "$pid_file"
}

stop_existing_service() {
  stopped_any=0
  if stop_user_systemd_if_running; then echo "stopped existing systemd --user service"; stopped_any=1; fi
  if stop_system_systemd_if_running; then echo "stopped existing system service"; stopped_any=1; fi
  if stop_initd_if_running; then echo "stopped existing init.d service"; stopped_any=1; fi
  if stop_background_if_running; then echo "stopped existing background service"; stopped_any=1; fi
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
ExecStart=$DAEMON_BIN_PATH
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
ExecStart=$DAEMON_BIN_PATH
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
set -eu
. "__ENV_FILE__"
case "${1:-}" in
  start)
    mkdir -p "__RUN_DIR__"
    if [ -f "__RUN_DIR__/symtermd.pid" ] && kill -0 "$(cat "__RUN_DIR__/symtermd.pid")" 2>/dev/null; then exit 0; fi
    nohup "__BIN_PATH__" >>"__LOG_PATH__" 2>&1 &
    echo $! >"__RUN_DIR__/symtermd.pid"
    ;;
  stop)
    if [ -f "__RUN_DIR__/symtermd.pid" ]; then kill "$(cat "__RUN_DIR__/symtermd.pid")" 2>/dev/null || true; rm -f "__RUN_DIR__/symtermd.pid"; fi
    ;;
  restart)
    "$0" stop
    "$0" start
    ;;
  status)
    if [ -f "__RUN_DIR__/symtermd.pid" ] && kill -0 "$(cat "__RUN_DIR__/symtermd.pid")" 2>/dev/null; then exit 0; fi
    exit 1
    ;;
  *)
    exit 2
    ;;
esac
EOF
  sed -i -e "s|__ENV_FILE__|$ENV_FILE|g" -e "s|__RUN_DIR__|$RUN_DIR|g" -e "s|__BIN_PATH__|$DAEMON_BIN_PATH|g" -e "s|__LOG_PATH__|$LOG_PATH|g" "$INITD_PATH"
  chmod 0755 "$INITD_PATH"
  service "$SERVICE_NAME" restart
  echo "initd" >"$STATE_FILE"
}

install_background() {
  mkdir -p "$RUN_DIR"
  if [ -f "$RUN_DIR/symtermd.pid" ] && kill -0 "$(cat "$RUN_DIR/symtermd.pid")" 2>/dev/null; then
    kill "$(cat "$RUN_DIR/symtermd.pid")" 2>/dev/null || true
  fi
  nohup sh -c '. "$1"; exec "$2"' sh "$ENV_FILE" "$DAEMON_BIN_PATH" >>"$LOG_PATH" 2>&1 &
  echo $! >"$RUN_DIR/symtermd.pid"
  echo "background" >"$STATE_FILE"
}

install_client() {
  source_url="$SYMTERM_DOWNLOAD_URL"
  if ! has_value "$source_url"; then
    source_url="$(release_asset_url "symterm")"
  fi
  install_binary_from_source "$source_url" "symterm" "$CLIENT_BIN_PATH"
  CLIENT_LINK_PATH="$(link_command "$CLIENT_BIN_PATH" "symterm")"
}

determine_daemon_request() {
  case "$(parse_install_daemon_mode)" in
    yes)
      DAEMON_REQUESTED=1
      ;;
    no)
      DAEMON_REQUESTED=0
      ;;
    auto)
      if is_interactive_terminal; then
        default_choice=0
        if [ "$DAEMON_SUPPORTED" -eq 1 ]; then default_choice=1; fi
        DAEMON_REQUESTED="$(prompt_yes_no_default "Install symtermd daemon" "$default_choice")"
      else
        DAEMON_REQUESTED=0
      fi
      ;;
  esac
}

validate_remote_entry() {
  require_value "$SYMTERMD_REMOTE_ENTRY" "SYMTERMD_REMOTE_ENTRY"
  case "$SYMTERMD_REMOTE_ENTRY" in
    \[*\])
      ;;
    *)
      echo "SYMTERMD_REMOTE_ENTRY should be a JSON argv array, for example:" >&2
      echo "  export SYMTERMD_REMOTE_ENTRY='[\"bash\"]'" >&2
      exit 1
      ;;
  esac
}

install_daemon() {
  [ "$DAEMON_REQUESTED" -eq 1 ] || return 0
  if [ "$DAEMON_SUPPORTED" -ne 1 ]; then
    echo "warning: symtermd is not supported on $RELEASE_OS; skipping daemon install" >&2
    DAEMON_REQUESTED=0
    return 0
  fi
  load_existing_env_defaults
  run_setup_wizard
  validate_remote_entry
  stop_existing_service
  source_url="$SYMTERMD_DOWNLOAD_URL"
  if ! has_value "$source_url"; then
    source_url="$(release_asset_url "symtermd")"
  fi
  install_binary_from_source "$source_url" "symtermd" "$DAEMON_BIN_PATH"
  write_env_file
  DAEMON_LINK_PATH="$(link_command "$DAEMON_BIN_PATH" "symtermd")"
  if command -v systemctl >/dev/null 2>&1 && systemctl --user --version >/dev/null 2>&1; then
    install_user_systemd
  elif command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    install_system_systemd
  elif command -v service >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    install_initd
  else
    install_background
  fi
  DAEMON_INSTALLED=1
}

print_summary() {
  echo "symterm $RESOLVED_VERSION installed"
  echo "repo: $REPO_URL"
  echo "client binary path: $CLIENT_BIN_PATH"
  echo "client command link: $CLIENT_LINK_PATH"
  if [ "$DAEMON_INSTALLED" -eq 1 ]; then
    echo "daemon binary path: $DAEMON_BIN_PATH"
    echo "daemon command link: $DAEMON_LINK_PATH"
    echo "service mode: $(cat "$STATE_FILE")"
  else
    echo "daemon install skipped"
  fi
  if ! path_contains_dir "$(dirname "$CLIENT_LINK_PATH")"; then
    echo "warning: $(dirname "$CLIENT_LINK_PATH") is not in PATH for this shell"
  fi
}

main() {
  maybe_reexec_with_bash "$@"
  parse_args "$@"
  normalize_repo_slug "$SYMTERM_REPO"
  detect_platform
  resolve_release_version
  install_client
  determine_daemon_request
  install_daemon
  print_summary
}

main "$@"
