# AGENTS.md

## Project Overview

**symterm** is a remote development workspace tool: a Go client/daemon pair for running commands against a shared project workspace over SSH.

- **`symterm`**: user-facing CLI. It attaches the current local working directory to a remote project, performs owner sync when needed, follows shared workspace state, and starts remote commands. It also exposes `admin`, `setup`, `completion`, and `version` commands.
- **`symtermd`**: Linux-oriented daemon. It owns per-user project state, workspace publication, command execution, SSH transport, the local admin socket, and the embedded admin web UI. Real shared workspace publication requires FUSE3; local development can run in passthrough mode with `SYMTERMD_ALLOW_UNSAFE_NO_FUSE=1`.

Core model: a **project** is keyed by `(username, project-id)` and has one shared workspace.

- The first authority session becomes the **owner** and performs the initial sync.
- Later clients attach as **followers** and use the already-published workspace.
- If a later attach appears to come from incompatible local content, the project enters `needs-confirmation` and blocks writes and command starts until a client reconnects with `--confirm-reconcile`.

Communication uses one SSH control connection carrying JSON-RPC plus separate binary-framed stdio channels for command I/O.

## Build And Test

```bash
# Go builds
go build -o symterm ./cmd/symterm
go build -o symtermd ./cmd/symtermd

# Go tests
go test ./...
go test ./internal/control/...
go test ./internal/control/ -run TestSessionRegistry

# Admin web
cd web/admin
npm ci
npm run lint
npm run test
npm run build

# Snapshot release builds
goreleaser build --snapshot --clean
./tools/goreleaser_build_snapshot.sh
.\tools\goreleaser_build_snapshot.ps1
```

Current module target is Go `1.25.0` in [go.mod](go.mod).

`symtermd` embeds the admin SPA from `internal/admin/webdist/` via `//go:embed`, so if a change touches `web/admin/`, rebuild the frontend before relying on daemon build results.

CI lives in `.github/workflows/ci.yml` and currently runs:

- Node LTS setup plus `npm ci`, `npm run lint`, `npm run test`, and `npm run build` in `web/admin`
- `gofmt` verification
- `go vet ./...`
- `go test ./...`
- `go build ./cmd/symterm` on Linux, macOS, and Windows
- `go build ./cmd/symtermd` on Linux
- CI version resolution plus `-ldflags` injection into built binaries
- artifact upload per platform

If a change clearly does not need CI, include `[ci skip]` in the commit message so the `push`/`pull_request` CI workflow is skipped.

Release automation is split across:

- `.github/workflows/version-bump.yml`: on pushes to `master`, computes the next patch tag, pushes it, and runs GoReleaser
- `.github/workflows/release.yml`: on pushed `v*` tags, runs GoReleaser
- `.goreleaser.yaml`: defines release archives, checksum output, snapshot versioning, and `before` hooks that build the admin web and run `go test ./...`

Platform coverage relies heavily on build tags such as `linux`, `windows`, and `!windows`. On Windows, daemon/FUSE-specific code paths use stubs.

## Architecture

### Entry points

- `cmd/symterm/main.go`: routes to the default client path, `run`, `completion`, `admin`, `setup`, `version`, and the internal authority-broker command
- `cmd/symtermd/main.go`: loads daemon config, handles `--version` and `--verbose`, dispatches the internal `tmux-status` helper, and starts `internal/cmd/daemon`

### Project Hierarchy

Use this as the fast path when locating code:

| Path | What lives there |
| --- | --- |
| `cmd/symterm/` | Thin CLI entrypoint and root command routing |
| `cmd/symtermd/` | Thin daemon entrypoint and daemon-only flags |
| `internal/cmd/client/` | Client runtime boot, SSH dial, sync progress TUI, authority broker, and top-level command execution flow |
| `internal/cmd/daemon/` | Daemon boot, SSH server, tmux status helper, and process lifecycle wiring |
| `internal/cmd/adminclient/` | Local admin CLI over the daemon Unix socket |
| `internal/cmd/completion/` | Shell completion script generation |
| `internal/cmd/setupwizard/` | Interactive client setup wizard |
| `internal/app/` | Client-side application flows: hello/open/ensure/resume/start-command, owner runtime bootstrapping, and stdio/session orchestration |
| `internal/control/` | Main business service layer and the best starting point for session/project/sync/command rules |
| `internal/project/` | Per-project state machine, owner/follower role changes, reconcile confirmation, and project snapshots |
| `internal/daemon/` | Workspace runtime implementation: mount management, publish mode, staged writes, PTY/tmux runner, and sync planning |
| `internal/diagnostic/` | Diagnostics reporter for runtime health and error tracking |
| `internal/invalidation/` | Invalidation change tracking for owner workspace coherence |
| `internal/sync/` | Client-side snapshotting, initial sync, owner workspace watch, persistent hash cache, and local owner FS runtime |
| `internal/transport/` | SSH transport, JSON-RPC codec/dispatch, stdio channels, keepalive, and ownerfs RPC forwarding |
| `internal/ssh/` | SSH endpoint parsing and proxy helpers |
| `internal/eventstream/` | Generic cursor-based append/subscribe event store |
| `internal/ownerfs/` | Client adapter for owner filesystem RPCs |
| `internal/projectready/` | Wait/refresh helper for projects that are not yet command-ready |
| `internal/portablefs/` | Cross-platform path normalization and path-safety helpers |
| `internal/workspaceidentity/` | Stable per-install workspace identity generation used during authority and reconcile decisions |
| `internal/admin/` | Admin store, socket RPC, HTTP server, API handlers, event hub, and embedded static asset serving |
| `internal/config/` | Client and daemon config parsing plus endpoint and env handling |
| `internal/proto/` | Shared wire types and error codes |
| `internal/setup/` | Daemon container assembly and project runtime facade wiring |
| `internal/buildinfo/` | Build version resolution and tests |
| `web/admin/` | React/Vite admin SPA source |
| `tools/` | Install, uninstall, and snapshot build helper scripts |
| `.github/workflows/` | CI, release, and version bump automation |

Common file-level jump points:

- `cmd/symterm/main.go` -> CLI route selection
- `internal/cmd/client/client.go` -> client process entry
- `internal/cmd/client/control_flow.go` -> main client attach/start flow
- `internal/control/service.go` -> service wiring root
- `internal/control/project_coordinator.go` -> project/session coordination
- `internal/daemon/workspace_manager.go` -> workspace runtime core
- `internal/daemon/workspace_manager_sync.go` -> sync planning and apply path
- `internal/daemon/workspace_manager_owner_authority.go` -> owner authority split and ownerfs binding
- `internal/daemon/workspace_manager_conservative.go` -> conservative mode entry for follower workspace safety
- `internal/sync/hash_cache.go` -> persistent hash cache for workspace snapshotting
- `internal/transport/ownerfs_rpc.go` -> ownerfs RPC forwarding with idle timeout
- `internal/admin/http_v1.go` -> admin API surface
- `internal/admin/static_assets.go` -> embedded admin UI serving
- `web/admin/src/pages/` -> admin UI screens (Overview, Sessions, Users, Audit, System)

### Key internal packages

| Package | Responsibility |
| --- | --- |
| `app` | Client-side application flows for hello/open/ensure/resume/start-command, owner runtime bootstrapping, and stdio/session orchestration |
| `control` | Core business service layer. `Service` wires authentication, session registry, project coordination, sync/filesystem control, invalidation, command control, and diagnostics |
| `transport` | SSH transport, JSON-RPC codec, generic dispatch routes, stdio channels, stream handlers, and ownerfs RPC forwarding |
| `daemon` | Workspace runtime implementation: mount management, sync receiver, staged writes, conservative mode, PTY runner, tmux launcher, and runtime lifecycle |
| `project` | Per-project state machine for owner/follower roles, authority rebinding, reconcile confirmation, command snapshots, and cursor-based project events |
| `sync` | Client-side workspace snapshotting, initial sync runner, fsnotify-based owner workspace watching, and local owner file service/runtime |
| `admin` | Admin store, token/user management, audit/event streams, Unix socket RPC, HTTP server, event hub, and embedded web UI hosting |
| `config` | Client and daemon configuration parsing, endpoint parsing, environment handling, and setup wizard config persistence |
| `proto` | Shared request/response types, enums, warning/error codes, and transport payload models |
| `eventstream` | Generic cursor-based append/subscribe event store |
| `ownerfs` | Client adapter for owner filesystem RPCs |
| `projectready` | Wait/refresh helper for projects that are not yet command-ready |
| `portablefs` | Cross-platform path normalization and path-safety helpers |
| `workspaceidentity` | Stable per-install workspace identity generation used during authority and reconcile decisions |
| `buildinfo` | Version injection and reporting |

### Design patterns

- **Interface-driven wiring**: `control.Service` accepts a `ServiceDependencies` bundle with runtime, diagnostics, clocks, session observers, and tracing hooks
- **Use-case surfaces**: `internal/control/service_surfaces.go` defines the service interfaces consumed by transport and admin layers
- **Generic JSON-RPC dispatch**: `internal/transport/dispatch_routes.go` uses generic helpers for typed parameter decoding and uniform responses
- **Cursor-based event streaming**: both `eventstream.Store` and admin/project event hubs expose append plus subscribe semantics with cursors
- **Owner authority split**: command start, sync, ownerfs, and invalidation paths distinguish between the active authority session and ordinary interactive followers
- **Embedded admin web**: `web/admin/` is the source tree, Vite output lands in `internal/admin/webdist/`, and the daemon serves it from the final binary
- **Platform abstraction with build tags**: Linux-specific FUSE, mount, and PTY implementations have non-Linux or Windows stubs where needed

### Data flow

1. Client dials the daemon's SSH endpoint and authenticates with a token.
2. `hello` establishes the client identity and supplies local workspace metadata, including digest and workspace instance ID.
3. `open_project_session` attaches to the project and returns the current project snapshot.
4. If the client is the authority owner and the project is syncing, the client performs initial sync through `begin_sync`, manifest planning, file transfer, and `finalize_sync`.
5. Once the project is active, commands start through `start_command` or `start_project_command_session`, and stdio is streamed over dedicated channels.
6. Owner-side filesystem watching produces invalidation events so published workspace state stays coherent for followers.

## Development Notes

- Prefer maintainability, structural clarity, and readability over minimal patches to legacy code.
- Treat `web/admin/` as the source of truth for the admin UI. `internal/admin/webdist/` is generated output consumed by `go:embed`; rebuild it when frontend code changes.
- The repo depends on more than `golang.org/x/crypto`; notable runtime dependencies include `fsnotify`, `go-fuse`, `pty`, `x/net`, `x/term`, and `x/text`. Check [go.mod](go.mod) before documenting dependency assumptions.
- `symtermd` is Linux-oriented and expects FUSE3 for real workspace publication. Use `SYMTERMD_ALLOW_UNSAFE_NO_FUSE=1` only for local development or tests.
- `internal/daemon/workspace_manager_test.go` remains a strong integration-style reference for sync and staged-write behavior; the nearby `workspace_manager_sync_*` tests cover newer sync planner scenarios.
- Build version defaults to `dev` in `internal/buildinfo/version.go` and is overridden in CI and release builds via `-ldflags`.
- Installer and uninstaller behavior lives in `tools/install-symterm.*` and `tools/uninstall-symterm.sh`; `tools/fuse-cleanup.sh` is a helper for force-unmounting stale FUSE mounts. If user-facing install docs change, verify those scripts too.
- Command execution persists logs under each project's `commands/` directory; use `README.md` as the current user-facing reference for on-disk layout and operational details.
- The repository includes a `docs/` directory for supplementary developer documentation such as performance optimization guides.
- `internal/sync/hash_cache.go` implements a persistent hash cache keyed by workspace instance ID to avoid re-hashing unchanged files across sync sessions.
- **CI skip rule**: for changes that do not affect compilation, tests, or build artifacts—such as pure documentation updates to `AGENTS.md`, `README.md`, or `docs/`—the commit message **must** include `[ci skip]` to avoid wasting CI runner time. Review the CI workflow scope in `.github/workflows/ci.yml` before deciding.
- When documentation and implementation diverge, treat the codebase as the source of truth.
