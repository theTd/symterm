# symterm

`symterm` is a Go client/daemon pair for running commands against a shared project workspace.

The client, `symterm`, attaches to a project, syncs or follows its workspace state, and can start commands inside that project runtime. The daemon, `symtermd`, owns project state, workspace publication, command execution, and admin surfaces.

## Components

- `symterm`: user-facing CLI. It connects to a daemon over the built-in SSH endpoint `symterm://<host>:<port>`.
- `symtermd`: project daemon. It manages per-user project state, workspace mounts, command sessions, and admin endpoints.
- `symterm admin`: local admin CLI over the daemon's Unix socket.

## How it works

Each project is scoped by `(username, project-id)`.

- The first attached client becomes the `owner`.
- The owner performs the initial workspace sync and activates the project.
- Later clients attach as `follower`s and use the already-published workspace.
- If another attach for the same project appears to come from incompatible local content, the project enters `needs-confirmation` and blocks writes and command starts until a client reconnects with `--confirm-reconcile`.

When a command is started, the daemon records stdout, stderr, and exit status under the project command log directory.

## Requirements

- Go `1.25.0` or newer.
- `symterm` builds on Linux, macOS, and Windows.
- `symtermd` is intended to run on Linux.
- Real remote workspace publication requires Linux plus FUSE3.
- For local development and tests, the daemon can run in passthrough mode with `SYMTERMD_ALLOW_UNSAFE_NO_FUSE=1`.

## Build

```bash
go build -o symterm ./cmd/symterm
go build -o symtermd ./cmd/symtermd
```

Run the test suite with:

```bash
go test ./...
```

The repository also includes GitHub Actions CI in `.github/workflows/ci.yml`. On every push and pull request it runs:

- `gofmt` verification
- `go vet ./...`
- `go test ./...`
- `go build ./cmd/symterm` on Linux, macOS, and Windows
- `go build ./cmd/symtermd` on Linux
- injects a CI-only build version into binaries
- uploads downloadable workflow artifacts for each platform build

CI versioning works like this:

- local non-CI builds keep the default `dev` version
- tag builds use the exact tag version, for example `v0.1.4`
- non-tag CI builds resolve to the next patch version plus a CI suffix, for example `v0.1.5-ci.1a2b3c4`

Release automation is split into two workflows:

- `.github/workflows/version-bump.yml`: on every push to `master`, creates the next patch tag (`v0.1.0`, `v0.1.1`, ...), pushes it, and immediately runs GoReleaser for that tag
- `.github/workflows/release.yml`: on every manually pushed `v*` tag, runs GoReleaser and publishes GitHub release assets

Builds inject the resolved version into both binaries. You can inspect it with:

```bash
./symterm version
./symtermd --version
```

## Quick start

### 1. Install the client and optionally start the daemon

To download and run the installer in one step on Unix-like hosts:

```bash
curl -fsSL https://raw.githubusercontent.com/theTd/symterm/master/tools/install-symterm.sh | bash
```

If `curl` is unavailable:

```bash
wget -qO- https://raw.githubusercontent.com/theTd/symterm/master/tools/install-symterm.sh | bash
```

To download and run the installer in one step on Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/theTd/symterm/master/tools/install-symterm.ps1 | iex
```

If you already have the repository checked out locally, use:

```bash
./tools/install-symterm.sh
```

On Windows, use:

```powershell
.\tools\install-symterm.ps1
```

Use the tracked installers at [tools/install-symterm.sh](/c:/Users/cui/standalone/symterm/tools/install-symterm.sh) and [tools/install-symterm.ps1](/c:/Users/cui/standalone/symterm/tools/install-symterm.ps1). The default repository is `https://github.com/theTd/symterm/`.

Both installers install the client first. After that they ask whether to install `symtermd`. On Linux, choosing yes continues into daemon setup and service installation. On unsupported platforms, the installer warns and skips daemon installation.

To uninstall on Unix-like hosts, use [tools/uninstall-symterm.sh](/c:/Users/cui/standalone/symterm/tools/uninstall-symterm.sh). By default it removes `symtermd` and then asks whether to also remove the `symterm` client and whether to purge daemon user data:

```bash
./tools/uninstall-symterm.sh
```

For non-interactive use, pass explicit flags such as `--remove-client`, `--keep-client`, `--purge-data`, or `--keep-data`. `--all --purge-data` removes both binaries and also deletes the daemon projects root such as `~/.symterm`:

```bash
./tools/uninstall-symterm.sh --all --purge-data
```

If you choose daemon install on Linux, the installer runs an interactive setup wizard by default. If an existing install already has `symtermd.env`, the wizard shows those current values as defaults. Use `./tools/install-symterm.sh --skip-setup-wizard` to reuse the current or pre-exported values without prompting.

If you plan to install the daemon on Linux, `SYMTERMD_REMOTE_ENTRY` defaults to `["bash"]`. You can still override it with a JSON argv array, for example:

```bash
export SYMTERMD_REMOTE_ENTRY='["bash"]'
```

For one-step daemon install on Linux with an explicit override, export it before piping the installer:

```bash
export SYMTERMD_REMOTE_ENTRY='["bash"]'
curl -fsSL https://raw.githubusercontent.com/theTd/symterm/master/tools/install-symterm.sh | bash
```

Optional release overrides:

- `SYMTERM_VERSION=v0.1.4` pins both binaries to a specific release tag instead of `latest`
- `SYMTERM_REPO=https://github.com/owner/repo/` installs from a different GitHub repository with the same GoReleaser asset naming
- `SYMTERM_DOWNLOAD_URL` or `SYMTERMD_DOWNLOAD_URL` override either binary source directly

On Unix-like hosts, the script is idempotent. Re-running it upgrades or repairs the same install. Before it overwrites the daemon binary, it checks for an already running local `symtermd` service and stops it first, then starts the newly installed version. It prefers:

- `systemd --user`
- system `systemd`
- `init.d/service`
- background mode

The Unix installer places real binaries under `~/.local/lib/symterm/bin/` by default and also creates command links, usually `~/.local/bin/symterm` and `~/.local/bin/symtermd` for user installs or `/usr/local/bin/` links for root installs. The Windows installer places `symterm.exe` under `%LOCALAPPDATA%\\symterm\\bin\\`.

On first start, `symtermd` generates and persists its ED25519 SSH host key at:

```text
<projects-root>/ssh_host_ed25519
```

### 2. Create users and issue tokens

```bash
symterm admin users create <username>
symterm admin users token issue <username>
```

The admin creates a user and sends that user's issued secret to the client user.

### 3. Write local client configuration

```bash
./symterm setup
```

The setup wizard writes `~/.symterm/config.json` with:

- daemon endpoint: `symterm://<host>:<port>`
- issued user token
- `known_hosts` strategy

### 4. Run commands normally

```bash
./symterm echo hello
```

The SSH username is always fixed to `symterm`.

## CLI usage

Root usage:

```text
symterm <remote-argv...>
symterm run [local-options] -- <remote-argv...>
symterm admin ...
symterm setup
```

Use `symterm <remote-argv...>` for normal daily execution.

Use `symterm run [local-options] -- <remote-argv...>` when you need explicit local control such as:

- `--project-id <id>`: override the default project id. If omitted, the client uses the current directory name.
- `--confirm-reconcile`: explicitly take over a project that is waiting for reconcile confirmation.
- tmux status mode is enabled by default for interactive TTY attaches. Set `SYMTERM_HIDE_TMUX_STATUS=1` to hide it globally.
- `--help`: show client help.

Use `symterm setup` to write local client configuration when the admin has already provided the endpoint and user secret.

If `<remote-argv...>` is omitted, `symterm` still starts the configured remote entrypoint with no extra argv tail.

If `symterm` starts without a usable environment and without `~/.symterm/config.json`, it returns a missing-configuration error that tells you to run `symterm setup`.

The client prints a short status summary such as:

```text
project=demo role=owner state=active cursor=3
command=cmd-0001 argv=2
exit=0
```

## Endpoint formats

`SYMTERM_ENDPOINT` supports:

- `symterm://host:port`
- `symterm://127.0.0.1:7000`
- `symterm://[2001:db8::1]:7000`

Legacy `exec://`, `ssh://`, `tcp://`, and `ssh-tcp-forward://` endpoints are rejected with a migration hint.

## Configuration

### Local config file

`symterm setup` writes one active client config file at:

```text
~/.symterm/config.json
```

That file stores:

- endpoint
- token
- host-key validation settings
- metadata with config version and update timestamp

In V1, `symterm` reads this file, but `symtermd` does not.

### Client environment

| Variable | Required | Meaning |
| --- | --- | --- |
| `SYMTERM_ENDPOINT` | No | Daemon endpoint in `symterm://<host>:<port>` form. Overrides the config file connection. |
| `SYMTERM_TOKEN` | No | SSH password used to authenticate as the fixed SSH user `symterm`. Overrides the config file token. |
| `SYMTERM_SSH_KNOWN_HOSTS` | No | Known-hosts file list for SSH host key validation. Empty string disables verification explicitly. |
| `SYMTERM_HIDE_TMUX_STATUS` | No | Set to `1`/`true`/`on` to disable the default tmux status bar for interactive commands. |

### Daemon environment

| Variable | Required | Meaning |
| --- | --- | --- |
| `SYMTERMD_PROJECTS_ROOT` | No | Root directory for project state. Defaults to `~/.symterm`. |
| `SYMTERMD_REMOTE_ENTRY` | No | Command prefix used for project commands. Accepts a single argv item or a JSON array. Defaults to `["bash"]`. |
| `SYMTERMD_REMOTE_ENTRY_ARGS_JSON` | No | Extra argv items for legacy single-item `SYMTERMD_REMOTE_ENTRY`. |
| `SYMTERM_TMUX_STATUS_LEFT` | No | Override tmux `status-left`. Default is ` {brand} | {user}@{project} `. Supports `{brand}`, `{user}`, `{project}`, `{role}`. |
| `SYMTERM_TMUX_STATUS_RIGHT` | No | Override tmux `status-right`. Supports `{status}` and `{clock}`. |
| `SYMTERMD_STATIC_TOKENS` | No | Comma-separated static credentials in `token=username` form. Prefer issuing per-user managed tokens through `symterm admin users token issue`. |
| `SYMTERMD_SSH_LISTEN_ADDR` | No | SSH listen address for the embedded business endpoint. Defaults to `127.0.0.1:7000`. |
| `SYMTERMD_ADMIN_WEB_ADDR` | No | Admin HTTP listen address. Defaults to `127.0.0.1:6040`. |
| `SYMTERMD_ALLOW_UNSAFE_NO_FUSE` | No | Enables passthrough workspace mode without FUSE. Intended for local testing. |

## Admin surfaces

The daemon starts two admin interfaces:

- Unix socket at `<projects-root>/admin.sock`
- HTTP admin server at `SYMTERMD_ADMIN_WEB_ADDR`

The web UI is served at `/admin`. Login is loopback-only and session-cookie based.

The CLI admin entrypoint is:

```text
symterm admin ...
```

Supported commands:

```text
symterm admin daemon info
symterm admin sessions list
symterm admin sessions inspect <session-id>
symterm admin sessions terminate <session-id>
symterm admin users list
symterm admin users create <username>
symterm admin users disable <username>
symterm admin users token issue <username>
symterm admin users token revoke <token-id>
symterm admin users entrypoint get <username>
symterm admin users entrypoint set <username> -- <argv>
```

If needed, point the admin client at a non-default socket with `SYMTERM_ADMIN_SOCKET`.

## On-disk layout

For a project `(username, project-id)`, the daemon creates:

```text
<projects-root>/<username>/<project-id>/
  workspace/
  mount/
  runtime/
  commands/
```

Each command gets its own subdirectory under `commands/` with:

- `stdout.log`
- `stderr.log`
- `exit_code.txt`

Admin state lives under:

```text
<projects-root>/admin/
```

That store contains user records, token records, and an `audit.log`.

## Releases

This repository uses GoReleaser.

- `symterm` release archives are built for Linux, macOS, and Windows on `amd64` and `arm64`.
- `symtermd` release archives are built for Linux on `amd64` and `arm64`.

For local snapshot builds:

```bash
goreleaser build --snapshot --clean
```

Or use the repo scripts:

```powershell
.\tools\goreleaser_build_snapshot.ps1
```

```bash
./tools/goreleaser_build_snapshot.sh
```

See [RELEASING.md](RELEASING.md) for the exact release workflow.
