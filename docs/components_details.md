# BARGE — Components, Current State & Roadmap

This document is the single source of truth for the BARGE project: every
component, its purpose and implementation, what works today, and what is still
missing compared to Docker.

---

## Current State (Phase 2 Complete)

### What Works Today

| Command | Status |
|---------|--------|
| `barge version` | Working |
| `barge pull <image>` | Working — MCR, Docker Hub, private registries |
| `barge images` | Working |
| `barge rmi <image>` | Working |
| `barge run` | Working — detach, port publish, volume mount, env vars, env-file, memory limit |
| `barge ps [-a]` | Working |
| `barge stop <id>` | Working |
| `barge rm [-f] <id>` | Working |
| `barge logs [-f] <id>` | Working — tails log file written by detached container |
| `barge exec [-i] <id> <cmd>` | Working — interactive and non-interactive |
| `barge build [-t] [--build-arg]` | Working — executes Bargefile instructions |
| `barge commit <id> <image>` | Working — snapshots container filesystem as new image |
| `barge login [registry]` | Working — saves credentials to `%ProgramData%\barge\config.json` |
| `barge logout [registry]` | Working |
| `barge tag <src> <dst>` | Working |
| `barge push <image>` | Working |
| `barge stats <id>` | Partial — shows metric type, no CPU/memory values yet |
| `barge inspect <id>` | Partial — dumps raw JSON, no formatted summary |

### Bargefile Instructions

| Instruction | Status |
|-------------|--------|
| `FROM` | Working |
| `ARG` | Working — with and without default values |
| `WORKDIR` | Working |
| `COPY src dst` | Working — via HTTP zip transfer (requires PowerShell in image) |
| `RUN` | Working — runs via `cmd.exe`, respects WORKDIR |
| `ENV` | Working — `KEY=VALUE` and `KEY VALUE` forms |
| `EXPOSE` | Working — multiple ports |
| `CMD` | Working — JSON array and plain text forms |

### Supported Base Image Registries

| Registry | Example |
|----------|---------|
| Microsoft Container Registry | `mcr.microsoft.com/windows/servercore:ltsc2022` |
| Docker Hub | `python:3.12-windowsservercore-ltsc2022` |
| Private (Azure CR, etc.) | `mycompany.azurecr.io/myapp:latest` — use `barge login` first |

**Image requirement:** Must be a Windows container image (`windows/amd64`). Linux
images are rejected at runtime by the Windows container stack.

---

## Component 1 — The CLI (`cmd/barge/main.go`)

### What it is

The CLI is the only entry point a user touches. Every subcommand is registered
here; all business logic lives in `internal/`. Cobra handles flag parsing, help
generation, and exit codes.

### Framework: Cobra

BARGE uses [Cobra](https://github.com/spf13/cobra) — the same CLI framework used
by Docker, kubectl, and Hugo. It handles subcommands, flags, and `--help` output.

### The Preflight Gate

Every subcommand inherits a `PersistentPreRunE` hook that runs `preflight.Check()`
before any command executes. Commands that don't touch the daemon (`version`,
`help`, `completion`) are skipped. This gives users a plain-English error instead
of a cryptic pipe failure when containerd is not running.

### Command Reference

| Command | Flags | Delegates to |
|---------|-------|-------------|
| `version` | — | `client.Version()` |
| `pull <image>` | — | `client.Pull()` |
| `images` | — | `client.ListImages()` |
| `rmi <image...>` | — | `client.RemoveImage()` |
| `run <image>` | `--name`, `-d`, `--rm`, `-e`, `--env-file`, `-v`, `-p`, `--isolation`, `--memory`/`-m` | `client.Run()` |
| `ps` | `-a` | `client.ListContainers()` |
| `stop <id...>` | — | `client.StopContainer()` |
| `rm <id...>` | `-f` | `client.RemoveContainer()` |
| `logs <id>` | `-f` | `client.Logs()` |
| `exec <id> <cmd...>` | `-i` | `client.Exec()` |
| `build [dir]` | `-t`, `-f`, `--build-arg` | `build.Builder.Build()` |
| `commit <id> <image>` | — | `client.CommitContainer()` |
| `login [registry]` | — | `client.Login()` |
| `logout [registry]` | — | `client.Logout()` |
| `tag <src> <dst>` | — | `client.TagImage()` |
| `push <image>` | — | `client.PushImage()` |
| `stats <id>` | — | `client.Stats()` |
| `inspect <id>` | `--image` | `client.InspectContainer/Image()` |

### The `run` Command — Flag Detail

```
barge run [flags] <image> [command args...]
```

Everything after the image name is treated as the container command and its
arguments. No `--` separator is needed — cobra stops flag parsing at the first
non-flag positional argument (the image name) because `SetInterspersed(false)` is
set. This means flags like `-t` and `-n` that belong to the container command pass
through correctly:

```
barge run myimage:latest powershell -Command Get-Process
barge run -d tessa1:v1 ping -t 127.0.0.1
```

`--isolation` (default: `hyperv`) maps to `IsolationHyperV` or `IsolationProcess`
and flows into the OCI spec via `withHyperVIsolation()`.

`--memory` / `-m` sets the Hyper-V VM memory limit. Accepts human-readable
suffixes: `512m`, `2g`, `1024k`, or a raw byte count. Stored as
`s.Windows.Resources.Memory.Limit` (uint64, bytes) in the OCI spec.
Without this flag, Hyper-V uses dynamic memory — the VM starts with a minimum
allocation (~512 MB) and the host assigns more as needed, up to available RAM.

`--env-file` reads a file of `KEY=VALUE` lines, skipping `#` comments and blank
lines. Lines are merged with `-e` flags; `-e` takes precedence.

Volume mounts (`-v`) accept:
- `C:\host\path:C:\container\path` — read-write
- `C:\host\path:C:\container\path:ro` — read-only

Port mappings (`-p`) accept: `hostPort:containerPort`.

**Container naming and re-use:** if `--name X` is given and a container named `X`
already exists (even in stopped state), `barge run` fails with a clear error and
instructs the user to `barge rm X` first. Detached containers persist until
explicitly removed; foreground containers are always auto-removed on exit.

---

## Component 2 — Preflight Checks (`internal/preflight/check.go`)

### What it is

A single `Check()` function that verifies the runtime environment before any
command executes. It confirms:

1. **containerd is running** — attempts a gRPC connection to the containerd socket
2. **`barge-nat` HCN network exists** — the NAT network containers are attached to

If either check fails, the error message tells the user exactly what to run to fix
it. No other component needs to handle "daemon is down" because preflight catches
it first.

---

## Component 3 — Runtime Interface (`internal/client/interface.go`)

### What it is

A Go interface (`Runtime`) that every external operation in BARGE implements
against. The concrete type is `*Client`. A compile-time assertion:

```go
var _ Runtime = (*Client)(nil)
```

ensures the struct never drifts from the interface.

### Why an interface?

It decouples the CLI from containerd. Tests can provide a mock `Runtime` without
a live containerd daemon. The interface also makes the full capability surface of
BARGE visible in one place.

### Interface surface

```go
type Runtime interface {
    Version(ctx) (string, error)
    Pull(ctx, ref) (Image, error)
    ListImages(ctx) ([]Image, error)
    RemoveImage(ctx, ref) error
    TagImage(ctx, src, dst) error
    PushImage(ctx, ref) error
    Run(ctx, RunOptions) (string, error)
    ListContainers(ctx, all) ([]ContainerInfo, error)
    StopContainer(ctx, id) error
    RemoveContainer(ctx, id, force) error
    Logs(ctx, id, follow) error
    Exec(ctx, ExecOptions) error
    CommitContainer(ctx, id, ref, CommitOptions) error
    Stats(ctx, id) error
    Login(ctx, registry, username, password) error
    Logout(ctx, registry) error
}
```

---

## Component 4 — Container Lifecycle (`internal/client/run.go`, `container.go`)

### Run (`run.go`)

`Run()` creates and starts a container in a single operation:

1. Resolves the local snapshot for the image
2. Creates an OCI spec with `oci.GenerateSpec()` + Windows-specific modifiers
3. Attaches HCN network endpoint (port NAT rules applied here)
4. Applies VSMB volume mounts (empty `Type` field — runhcs converts to VSMB)
5. Creates the container and its task
6. For detached containers: creates log dir, uses `cio.LogFile(path)`, stores log
   path in container label `barge.logfile`
7. For foreground containers: uses `cio.WithStdio`, waits for exit

**Key labels stored on every container:**

| Label | Value |
|-------|-------|
| `barge.logfile` | Absolute path to the container's log file |
| `barge.endpoint` | HCN endpoint ID (cleaned up on `rm`) |

**Volume mounts** use an empty OCI `Mount.Type` field. runhcs (the containerd
shim) interprets this as VSMB for Hyper-V isolation — the correct Windows
mechanism. Using `Type: "bind"` (Linux concept) is rejected by runhcs.

**Port publishing** creates a WinNAT policy on the HCN endpoint mapping
`hostPort` → `containerPort`. The container's IP is assigned by HCN when the
endpoint is attached.

### Logs (`container.go`)

`Logs()` reads the `barge.logfile` label from the container's stored metadata,
then:

- Without `-f`: copies the entire file to stdout
- With `-f`: seeks forward every 500 ms printing new bytes (tail-follow)

This works because `cio.LogFile()` keeps the log file open and continuously
appends stdout/stderr from the container process.

### Exec (`container.go`)

`Exec()` creates a new process inside an existing container task. Always uses
`cio.WithStdio` so output is never suppressed. `pspec.Terminal = true` only when
`-i` is passed (enables PTY). The exec process exits independently of the
container's main process.

---

## Component 5 — Image Management (`internal/client/images.go`)

### Pull

Uses containerd's built-in fetch+unpack pipeline with the Docker resolver.
The resolver calls `credentialsForHost()` (see Component 6) automatically for
authenticated registries. Platform is pinned to `windows/amd64`.

### Tag

Creates a new image record in containerd's image store pointing to the same
content descriptor as the source. No data is copied.

### Push

Uses containerd's Push pipeline with the same Docker resolver used for Pull.
Credentials are looked up from the auth store automatically.

---

## Component 6 — Auth (`internal/client/auth.go`)

### Storage

Credentials are stored at `%ProgramData%\barge\config.json`:

```json
{
  "auths": {
    "registry-1.docker.io": {
      "username": "alice",
      "password": "secret"
    },
    "mycompany.azurecr.io": {
      "username": "alice",
      "password": "token"
    }
  }
}
```

`barge login` prompts for username (printed to stdout) and reads the password
silently using `golang.org/x/term.ReadPassword()` — no password echoed to
terminal.

### `credentialsForHost(host)`

A package-level function that reads the config file and returns the stored
username/password for a given registry host. It is passed to the Docker resolver
via `dockerresolver.ResolverOptions.Credentials` and called automatically on
every registry operation (pull, push).

---

## Component 7 — Build System (`internal/build/`)

### Bargefile Parser (`bargefile.go`)

`Parse(r io.Reader)` reads a Bargefile line by line:

- Skips blank lines and `#` comments
- Splits each line into `INSTRUCTION rest`
- Validates instruction-specific argument counts and formats
- Returns a `*Bargefile` with an ordered `[]Instruction` slice
- Errors if the first instruction is not `FROM`

**ENV** normalises both `KEY=VALUE` and `KEY VALUE` to `KEY=VALUE` internally.
**CMD** accepts a JSON array (`["cmd.exe", "/c", "app.exe"]`) or plain words.
**EXPOSE** splits multiple ports into separate args (`EXPOSE 80 443 8080`).
**ARG** stores the raw string (`NAME=default` or `NAME`) for the builder to parse.

### Builder (`builder.go`)

`Builder.Build()` executes the parsed instructions:

1. Pre-loads `--build-arg` overrides into `buildState.args`
2. Scans instructions for `ENV` and `CMD` — collected for commit time
3. Pulls the base image (`FROM`)
4. Starts a long-lived build container running `ping -t 127.0.0.1` (keeps it alive)
5. Loops over remaining instructions:
   - **ARG**: sets default in `state.args` if not already overridden
   - **WORKDIR**: updates `state.workDir`
   - **EXPOSE**: accumulates ports in `state.exposed`
   - **COPY**: calls `execCopy()` — HTTP file server + PowerShell download
   - **RUN**: calls `execRun()` — prefixed with `cd /d <workdir>` if WORKDIR is set
6. Stops the build container
7. Calls `CommitContainer()` with `OverrideCmd`, `WorkingDir`, `ExposedPorts`

**ARG substitution** (`substituteArgs`): replaces `${NAME}` (braced) then `$NAME`
(bare) using the accumulated args map. Applied to COPY, RUN, WORKDIR, EXPOSE
values at execution time.

### File Server (`fileserver.go`)

`COPY` cannot use bind mounts in Hyper-V isolation (the host filesystem is not
visible inside the VM boundary). Instead:

1. `zipDir(src)` archives the source directory into an in-memory zip buffer
2. `newFileServer(src)` starts a one-request HTTP server on a random port
3. The container execs PowerShell:
   ```powershell
   New-Item -ItemType Directory -Force 'C:\dst'
   Invoke-WebRequest -Uri 'http://<hostIP>:<port>/archive.zip' -OutFile C:\__barge_copy.zip
   Expand-Archive -Path C:\__barge_copy.zip -DestinationPath 'C:\dst' -Force
   Remove-Item C:\__barge_copy.zip -Force
   ```
4. The server shuts down after the request is served

The host IP is discovered via `network.GatewayIP()` — the NAT gateway of the
`barge-nat` HCN network, which is reachable from inside the container.

**COPY requires PowerShell.** NanoServer images do not include PowerShell — use
ServerCore-based images for any Bargefile that contains a `COPY` instruction.

---

## Component 8 — Network (`internal/network/hcn.go`)

### What it is

BARGE creates and manages a single HCN (Host Compute Network) NAT network named
`barge-nat`. All containers are attached to this network. HCN is the Windows API
for container networking — the equivalent of Linux's bridge/iptables stack.

### `EnsureNetwork()`

Called during container creation. If `barge-nat` doesn't exist, creates it with:
- Type: `NAT`
- Subnet: configurable CIDR (default `172.22.0.0/16`)
- NAT policy allowing outbound internet access

### `CreateEndpoint(networkID)`

Creates an HCN endpoint (virtual NIC) attached to `barge-nat`. HCN assigns the
container an IP address from the subnet. The endpoint ID is stored in the
`barge.endpoint` container label so it can be cleaned up on `barge rm`.

### `GatewayIP()`

Returns the host-side IP of the `barge-nat` network (the NAT gateway). Used by
the build file server so the container knows which IP to download from. First
tries the route `NextHop` for `0.0.0.0/0`; falls back to `subnet.IP + 1`.

### Port Publishing

Port NAT rules are added to the endpoint as HCN port-mapping policies:
`hostPort` → `containerPort`. WinNAT handles the actual packet rewriting.

---

## Component 9 — Output (`internal/output/`)

A thin wrapper around `fmt` and `os.Stderr`/`os.Stdout` that provides consistent
prefixed, coloured output:

| Function | Prefix | Stream |
|----------|--------|--------|
| `output.Infof` | `▸` (blue) | stdout |
| `output.Successf` | `✓` (green) | stdout |
| `output.Errorf` | `✗` (red) | stderr |
| `output.Warnf` | `⚠` (yellow) | stderr |

All formatting is disabled when stdout is not a TTY (e.g. piped to a file).

---

## Component 10 — Commit (`internal/client/commit.go`)

### What it is

`CommitContainer()` snapshots a stopped container's filesystem as a new image.

### How it works

1. Reads the container's snapshot using `snapshotter.Stat()` and `Mounts()`
2. Calls `DiffService().Compare()` to compute the VHD layer diff (the delta
   between the base snapshot and the current state)
3. Reads the base image manifest and config from the content store
4. Writes a new config blob with `WorkingDir`, `ExposedPorts`, and `Cmd` applied
5. Writes a new manifest referencing the original layers plus the new diff layer
6. Registers the new manifest as an image in the image store

### CommitOptions

```go
type CommitOptions struct {
    OverrideCmd  []string // sets CMD in the new image config
    WorkingDir   string   // sets WORKDIR in the new image config
    ExposedPorts []string // sets EXPOSE in the new image config
}
```

Used by both `barge commit` (user-facing) and `barge build` (automatic at the end
of every build).

---

## Windows-Specific Behaviors

These behaviors differ from Docker on Linux and are not bugs:

| Behavior | Reason |
|----------|--------|
| `COPY` requires PowerShell inside the image | Hyper-V isolation prevents host bind mounts; BARGE uses HTTP zip transfer instead. NanoServer images lack PowerShell — use ServerCore. |
| Detached containers use `NullIO` (no log file) | `containerd-shim-runhcs-v1` rejects `file://` URIs; only `binary://` or null I/O is accepted. Output from `RUN` steps is still visible via `Exec` which uses host stdio. |
| Committed image-layer snapshots cannot be mounted directly | Only `active` or `view` snapshots are mountable on Windows. `CommitContainer` creates a temporary view snapshot for the diff computation, then removes it. |
| OCI DiffID must be SHA256 of the **uncompressed** tar | The Windows diff plugin does not set the `containerd.io/uncompressed` annotation. BARGE decompresses the gzip blob on the fly to compute the correct DiffID. |
| Foreground containers are always auto-removed on exit | Leaving a stopped foreground container's snapshot in the store would prevent re-running with the same name. Detached containers persist until `barge rm`. |
| Hyper-V memory is dynamic by default | Without `--memory`, HCS assigns the VM ~512 MB minimum and grows it as needed. Use `-m 2g` to set a hard cap. |

---

## v0.1.0 Bug Fix Summary

All bugs encountered and fixed during initial end-to-end testing. Full details in `tsg/bugs/v0.1.0.md`.

| Bug | Symptom | Root Cause | Fix |
|-----|---------|-----------|-----|
| BUG-01 | `unknown shorthand flag: 'f' in -f` | `StringVar` instead of `StringVarP` | Changed to `StringVarP` |
| BUG-03 | `invalid port ":3.11-..."` | Short Docker Hub refs parsed as URLs | Added `normalizeRef()` in `images.go` |
| BUG-04 | `scheme must be 'binary', got: 'file'` | `cio.LogFile` rejected by Windows shim | Detached containers use `cio.NullIO` |
| BUG-05 | `invalid archive entry '.'` (single-file COPY) | `filepath.Rel(file, file)` returns `.` | `zipDir()` now handles files vs directories separately |
| BUG-06 | `The system cannot find the path specified` after WORKDIR | WORKDIR recorded but directory not created | `WORKDIR` now execs `New-Item -Force` in the container |
| BUG-07 | `invalid archive entry '.'` (relative dst) | `./` not resolved against WORKDIR | Added `resolveCopyDst()` |
| BUG-08 | `snapshot not active or view: failed precondition` | Called `Mounts()` on committed snapshot | Use a temporary view snapshot for diff lower |
| BUG-09 | Locally built images not found by `barge run` | `normalizeRef` always applied, but local builds use original name | `GetImage` tries normalized, then falls back to original |
| BUG-10 | `content digest: not found` after build | Image record created but never `Unpack()`'d | Call `img.Unpack(ctx, windowsSnapshotter)` after commit |
| BUG-11 | `wrong diff id calculated on extraction` | Windows diff plugin omits `containerd.io/uncompressed` annotation | `uncompressedDigest()` decompresses gzip blob, hashes raw tar |
| BUG-12 | `content digest: not found` (GC eviction) | Delete→Create race window allowed GC to evict config blob | `gc.root` labels on all blobs + atomic Create/Update image record |
| BUG-13 | `snapshot already exists` on re-run | Foreground containers left snapshot behind after exit | Foreground containers always auto-cleanup on exit |
| BUG-14 | `unknown shorthand flag: 't'` for container args | Cobra parsed container flags as barge flags | `SetInterspersed(false)` on `run` and `exec` commands |
| BUG-15 | `container named "X" already exists` (no remedy shown) | Detached stopped containers leave record+snapshot | `Run()` checks for existing container and returns actionable error |

---

## What Is Not Yet Implemented

### Exec + Commit Workflow (Live Container Modification)

**Status: Not implemented — not tested**

The pattern of attaching to a running container, making changes interactively, and
committing the result is not supported in the current BARGE build. `barge exec`
works against running containers but `barge commit` requires a **stopped**
container (the snapshotter cannot safely diff a live writable layer).

To experiment with a container's contents interactively:
1. Run the container: `barge run -d <image>`
2. Open a shell: `barge exec -i <id> cmd.exe`
3. Make changes
4. Stop the container: `barge stop <id>`
5. Commit: `barge commit <id> <new-image>`

### Terminal Resize in `exec -i`

PTY sessions work but window resize events (SIGWINCH) are not forwarded to the
container process. The terminal stays at the size it was at attach time.

### `barge stats` — Full Metrics

`barge stats` currently shows the metric type URL from the containerd metrics
response. Parsing the HCS (Host Compute System) protobuf into human-readable
CPU %, memory MB, and disk I/O is not yet implemented.

### `barge inspect` — Formatted Output

`barge inspect` dumps raw JSON. A formatted summary (IP address, ports, mounts,
image, status) is not yet implemented.

### Container-to-Container Networking

All containers share the `barge-nat` HCN network and can technically reach each
other by IP. However:
- Container IPs are not currently displayed (needs `barge inspect` formatted output)
- There is no DNS / name resolution between containers
- There are no named networks to isolate groups of containers

---

## Docker Feature Parity Gaps

Features present in Docker that BARGE does not yet have:

### Bargefile / Build

| Docker Feature | BARGE Status |
|---------------|-------------|
| `ENTRYPOINT` | Not implemented |
| `LABEL` | Not implemented |
| `VOLUME` | Not implemented |
| `USER` | Not implemented |
| `HEALTHCHECK` | Not implemented |
| `SHELL` | Not implemented |
| `ONBUILD` | Not implemented |
| Multi-stage builds (`FROM ... AS stage`) | Not implemented |
| Build cache | Not implemented — every build is a full rebuild |
| `.bargeignore` (equivalent of `.dockerignore`) | Not implemented |

### Runtime

| Docker Feature | BARGE Status |
|---------------|-------------|
| `docker cp` — copy files to/from running container | Not implemented |
| `docker top` — list processes in container | Not implemented |
| `docker diff` — show filesystem changes | Not implemented |
| `docker pause` / `docker unpause` | Not implemented |
| `docker rename` | Not implemented |
| `docker update` — update resource limits | Not implemented |
| `docker wait` — block until container exits | Not implemented |
| `docker events` — real-time event stream | Not implemented |
| `docker port` — list port mappings | Not implemented |
| `docker export` / `docker import` | Not implemented |
| `docker save` / `docker load` | Not implemented |
| `--memory` memory limit | Implemented — `barge run -m 2g` |
| `--cpus` CPU quota | Not implemented |
| Restart policies (`--restart always`) | Not implemented |
| `--user` flag | Not implemented |

### Networking

| Docker Feature | BARGE Status |
|---------------|-------------|
| Named networks (`docker network create`) | Not implemented |
| Container DNS / name resolution | Not implemented |
| `docker network ls / inspect / rm` | Not implemented |
| `--network host` / `--network none` | Not implemented |
| Network aliases | Not implemented |

### Compose

| Docker Feature | BARGE Status |
|---------------|-------------|
| `docker compose up/down/ps/logs` | Not implemented — `barge compose` does not exist yet |
| Declarative multi-container apps | Not implemented |
| Service dependencies (`depends_on`) | Not implemented |
| Shared named volumes across containers | Not implemented |

### Volumes

| Docker Feature | BARGE Status |
|---------------|-------------|
| Named volumes (`docker volume create`) | Not implemented — only host-path mounts via `-v` |
| `docker volume ls / inspect / rm` | Not implemented |
| tmpfs mounts | Not implemented |

---

## Roadmap — Suggested Next Steps

| Priority | Feature | Why |
|----------|---------|-----|
| High | `barge inspect` formatted output | Exposes container IP — unlocks manual container networking today |
| High | `barge compose` | Multi-container apps — the biggest usability gap |
| Medium | Container DNS / named networks | Containers find each other by name |
| Medium | `barge stats` full metrics | Operational visibility |
| Medium | `.bargeignore` | Avoid copying node_modules, .git etc into COPY |
| Medium | Build cache | Speed up iterative builds |
| Low | `ENTRYPOINT` in Bargefile | Common Dockerfile pattern |
| Low | `docker cp` equivalent | Useful for debugging |
| Low | Restart policies | Production reliability |
