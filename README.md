# BARGE — Windows Container Runtime

A beginner-friendly Windows container tool. Run Windows applications in isolated
Hyper-V containers using images from Docker Hub and the Microsoft Container
Registry (mcr.microsoft.com).

---

## Prerequisites

Run once on a new machine (requires administrator privileges):

```
barge init
```

This enables Hyper-V, enables the Windows Containers feature, installs
containerd, and starts the containerd service. A reboot may be required after
the first run — `barge init` will tell you if so, and you re-run it after
rebooting to complete the remaining steps.

---

## Quick Start

```
barge pull mcr.microsoft.com/windows/servercore:ltsc2022
barge run mcr.microsoft.com/windows/servercore:ltsc2022 cmd.exe
```

---

## Building an Image

Write a `Bargefile` (same syntax as a Dockerfile, Windows paths):

```dockerfile
FROM python:3.11-windowsservercore-ltsc2022
WORKDIR C:\app
COPY . .
RUN pip install -e .
CMD ["python", "main.py"]
```

Build it:

```
barge build -f Bargefile -t myapp:v1 .
```

---

## Development Workflow

There are two modes depending on what you are doing.

### Mode 1 — Development (volume mount)

Build the image **once** to install the environment (Python, packages, system
dependencies). Then mount your live source directory over the image's app
directory so edits on the host are immediately visible inside the container —
no rebuild needed.

```
barge build -f Bargefile -t myapp:v1 .

barge run --name myapp -v C:\path\to\myapp:C:\app myapp:v1 cmd.exe
```

Inside the container you get a shell at `C:\app` with your live source files.
Edit on the host, re-run inside the container, repeat.

**Rebuild only when the environment changes** — new packages in
`pyproject.toml`, a new `RUN` step, a base image update, etc.

### Mode 2 — Testing (baked image)

Rebuild the image with the latest source baked in, start it detached, then
attach a shell to test:

```
barge build -f Bargefile -t myapp:v1 .

barge run -d --name myapp myapp:v1 ping -t 127.0.0.1

barge exec -i myapp cmd.exe
```

The `ping -t 127.0.0.1` keep-alive is needed when the container's default
command exits quickly (e.g. a CLI tool). A long-running process such as a web
server keeps the container alive on its own and does not need it.

When done:

```
barge rm -f myapp
```

---

## Common Commands

| Command | Description |
|---------|-------------|
| `barge pull <image>` | Download an image |
| `barge images` | List local images |
| `barge rmi <image>` | Remove a local image |
| `barge build -t <tag> .` | Build an image from a Bargefile |
| `barge run <image>` | Run a container (foreground) |
| `barge run -d <image>` | Run a container (detached) |
| `barge run -m 2g <image>` | Run with a 2 GB memory limit |
| `barge ps` | List running containers |
| `barge ps -a` | List all containers |
| `barge stop <id>` | Stop a running container |
| `barge rm <id>` | Remove a stopped container |
| `barge rm -f <id>` | Force-remove a running container |
| `barge exec -i <id> cmd.exe` | Open a shell in a running container |
| `barge logs <id>` | Not yet working — detached containers use NullIO (see Notes) |
| `barge commit <id> <image>` | Save container state as a new image |
| `barge tag <src> <dst>` | Tag an image |
| `barge push <image>` | Push an image to a registry |
| `barge login <registry>` | Log in to a private registry |

---

## Bargefile Instructions

| Instruction | Example |
|-------------|---------|
| `FROM` | `FROM python:3.11-windowsservercore-ltsc2022` |
| `WORKDIR` | `WORKDIR C:\app` |
| `COPY` | `COPY . .` |
| `RUN` | `RUN pip install -e .` |
| `ENV` | `ENV KEY=VALUE` |
| `EXPOSE` | `EXPOSE 8080` |
| `CMD` | `CMD ["python", "main.py"]` |
| `ARG` | `ARG VERSION=1.0` |

`COPY` requires PowerShell inside the image. Use ServerCore-based images;
NanoServer does not include PowerShell.

---

## Notes

- All images must be Windows containers (`windows/amd64`). Linux images are
  rejected by the Windows container stack.
- Containers use Hyper-V isolation by default — each container gets its own
  Windows kernel instance. Use `--isolation process` for process isolation
  (faster startup, shares the host kernel).
- Detached containers (`-d`) persist until explicitly removed with `barge rm`.
  Foreground containers are always removed automatically on exit.
- Re-running with `--name X` when a container named `X` already exists fails
  with a clear error. Remove it first with `barge rm X`.
- `barge logs` does not currently work. The Windows container shim rejects
  `file://` log URIs, so detached containers use null I/O — their output is not
  captured to a file. To see output from a detached container today, use
  `barge exec -i <id> cmd.exe` and run the process interactively.
