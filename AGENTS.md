# AGENTS.md

## Project Overview

**symterm** is a remote development workspace tool: a Go client/daemon pair for running commands against a shared project workspace over SSH.

- **`symterm`** (client): user-facing CLI that attaches the current local working directory to a remote project, performs owner sync when needed, follows shared workspace state, and starts remote commands. It also contains the local `admin` CLI and the interactive `setup` wizard. It builds on Linux, macOS, and Windows.
- **`symtermd`** (daemon): Linux-oriented project daemon that owns per-user project state, workspace publication, command execution, SSH transport, and admin surfaces. Real shared workspace publication requires FUSE3; local development can run in passthrough mode with `SYMTERMD_ALLOW_UNSAFE_NO_FUSE=1`.

Core model: a **project** is keyed by `(username, project-id)` and has one shared workspace.

- The first authority session becomes the **owner** and performs the initial sync.
- Later clients attach as **followers** and use the already-published workspace.
- If a later attach appears to come from incompatible local content, the project enters `needs-confirmation` and blocks writes and command starts until a client reconnects with `--confirm-reconcile`.

Communication uses one SSH control connection carrying JSON-RPC plus separate binary-framed stdio channels for command I/O.

## Build And Test

```bash
# Build
go build -o symterm ./cmd/symterm
go build -o symtermd ./cmd/symtermd

# Test
go test ./...

# Single package
go test ./internal/control/...

# Single test
go test ./internal/control/ -run TestSessionRegistry

# Snapshot release builds
goreleaser build --snapshot --clean
./tools/goreleaser_build_snapshot.sh
.\tools\goreleaser_build_snapshot.ps1
```

Current module target is Go `1.25.0` in [go.mod](/c:/Users/cui/standalone/symterm/go.mod).

CI lives in `.github/workflows/ci.yml` and currently runs:

- `gofmt` verification
- `go vet ./...`
- `go test ./...`
- `go build ./cmd/symterm` on Linux, macOS, and Windows
- `go build ./cmd/symtermd` on Linux

Platform coverage in the tree relies heavily on build tags such as `linux`, `windows`, and `!windows`. On Windows, daemon/FUSE-specific code paths use stubs.

## Architecture

### Entry points

- `cmd/symterm/main.go`: routes to the default client path, `run`, `admin`, `setup`, `version`, and the internal authority-broker command.
- `cmd/symtermd/main.go`: loads daemon config, handles `--version`, and starts `internal/cmd/daemon`.

### Key internal packages

| Package | Responsibility |
|---------|---------------|
| `app` | Client-side application flows for hello/open/ensure/resume/start-command, owner runtime bootstrapping, and stdio/session orchestration. |
| `control` | Core business service layer. `Service` wires authentication, session registry, project coordination, sync/filesystem control, invalidation, command control, and diagnostics. |
| `transport` | SSH transport, JSON-RPC codec, generic dispatch routes, stdio channels, stream handlers, and ownerfs RPC forwarding. |
| `daemon` | Workspace runtime implementation: mount management, sync receiver, staged writes, conservative mode, PTY runner, tmux launcher, and runtime lifecycle. |
| `project` | Per-project state machine for owner/follower roles, authority rebinding, reconcile confirmation, command snapshots, and cursor-based project events. |
| `sync` | Client-side workspace snapshotting, initial sync runner, fsnotify-based owner workspace watching, and local owner file service/runtime. |
| `admin` | Admin store, token/user management, audit/event streams, Unix socket RPC, HTTP server, Web UI shell, and session adapters. |
| `setup` | Daemon container assembly and project runtime facade wiring. |
| `config` | Client and daemon configuration parsing, endpoint parsing, environment handling, and setup wizard config persistence. |
| `proto` | Shared request/response types, enums, warning/error codes, and transport payload models. |
| `ownerfs` | Client adapter for owner filesystem RPCs. |
| `eventstream` | Generic cursor-based append/subscribe event store. |
| `diagnostic` | Background error reporting helpers used by long-running services and cleanup paths. |
| `invalidation` | Helpers for generating workspace invalidation change sets. |
| `projectready` | Wait/refresh helper for projects that are not yet command-ready. |
| `workspaceidentity` | Stable per-install workspace identity generation used during authority and reconcile decisions. |
| `portablefs` | Cross-platform path normalization and path-safety helpers. |
| `ssh` | SSH endpoint helpers and client dialing support. |
| `buildinfo` | Version injection and reporting. |

### Design patterns

- **Interface-driven wiring**: `control.Service` accepts a `ServiceDependencies` bundle with runtime, diagnostics, clocks, session observers, and tracing hooks.
- **Use-case surfaces**: [internal/control/service_surfaces.go](/c:/Users/cui/standalone/symterm/internal/control/service_surfaces.go) defines the service interfaces consumed by transport and admin layers.
- **Generic JSON-RPC dispatch**: [internal/transport/dispatch_routes.go](/c:/Users/cui/standalone/symterm/internal/transport/dispatch_routes.go) uses generic helpers for typed parameter decoding and uniform responses.
- **Cursor-based event streaming**: both `eventstream.Store` and admin/project event hubs expose append plus subscribe semantics with cursors.
- **Owner authority split**: command start, sync, ownerfs, and invalidation paths distinguish between the active authority session and ordinary interactive followers.
- **Platform abstraction with build tags**: Linux-specific FUSE, mount, and PTY implementations have non-Linux or Windows stubs where needed.

### Data flow

1. Client dials the daemon's SSH endpoint and authenticates with a token.
2. `hello` establishes the client identity and supplies local workspace metadata, including digest and workspace instance ID.
3. `open_project_session` attaches to the project and returns the current project snapshot.
4. If the client is the authority owner and the project is syncing, the client performs initial sync through `begin_sync`, manifest planning, file transfer, and `finalize_sync`.
5. Once the project is active, commands start through `start_command` or `start_project_command_session`, and stdio is streamed over dedicated channels.
6. Owner-side filesystem watching produces invalidation events so published workspace state stays coherent for followers.

## Development Notes

- Prefer maintainability, structural clarity, and readability over minimal patches to legacy code.
- The repo now depends on more than `golang.org/x/crypto`; notable runtime dependencies include `fsnotify`, `go-fuse`, `pty`, `x/net`, `x/term`, and `x/text`. Check [go.mod](/c:/Users/cui/standalone/symterm/go.mod) before documenting dependency assumptions.
- `symtermd` is Linux-oriented and expects FUSE3 for real workspace publication. Use `SYMTERMD_ALLOW_UNSAFE_NO_FUSE=1` only for local development or tests.
- [internal/daemon/workspace_manager_test.go](/c:/Users/cui/standalone/symterm/internal/daemon/workspace_manager_test.go) is still the largest integration-style test file in the repo and remains the best reference for sync and staged-write behavior.
- Command execution persists logs under each project's `commands/` directory; see [README.md](/c:/Users/cui/standalone/symterm/README.md) for the current on-disk layout and admin surface details.
- [design.md](/c:/Users/cui/standalone/symterm/design.md) is the v1 design document in Chinese and remains useful for intent, but the codebase should win when there is any mismatch.
