package build

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asbassan/barge/internal/client"
	"github.com/asbassan/barge/internal/network"
	"github.com/asbassan/barge/internal/output"
)

// Builder executes Bargefile instructions against a live container and
// commits the result as a new image.
type Builder struct {
	cl client.Runtime
}

// NewBuilder creates a Builder backed by the given runtime client.
func NewBuilder(cl client.Runtime) *Builder {
	return &Builder{cl: cl}
}

// buildState tracks mutable state accumulated across Bargefile instructions.
type buildState struct {
	workDir string
	args    map[string]string
	exposed []string
}

// substituteArgs replaces ${NAME} and $NAME occurrences using the given arg map.
func substituteArgs(s string, args map[string]string) string {
	// Replace ${NAME} first to avoid partial replacement by $NAME pass.
	reBraced := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	s = reBraced.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1] // strip ${ and }
		if v, ok := args[name]; ok {
			return v
		}
		return m
	})

	reBare := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	s = reBare.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1:] // strip leading $
		if v, ok := args[name]; ok {
			return v
		}
		return m
	})
	return s
}

// Build executes every instruction in bf and tags the result as outputRef.
// contextDir is the root against which relative COPY source paths are resolved.
// buildArgs overrides ARG defaults in KEY=VALUE form.
func (b *Builder) Build(ctx context.Context, bf *Bargefile, contextDir, outputRef string, buildArgs []string) error {
	state := buildState{
		args: make(map[string]string),
	}

	// Pre-load build args so they can override ARG defaults.
	for _, kv := range buildArgs {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			state.args[parts[0]] = parts[1]
		}
	}

	baseImage := bf.Instructions[0].Args[0]

	// Collect ENV and CMD that are applied at commit time.
	var envVars []string
	var overrideCmd []string
	for _, instr := range bf.Instructions[1:] {
		switch instr.Type {
		case InstrENV:
			envVars = append(envVars, substituteArgs(instr.Args[0], state.args))
		case InstrCMD:
			overrideCmd = instr.Args
		}
	}

	// Step 1 — pull base image.
	output.Infof("Step 1/N: FROM %s", baseImage)
	if _, err := b.cl.Pull(ctx, baseImage); err != nil {
		return fmt.Errorf("FROM: %w", err)
	}

	// Step 2 — start a detached build container that stays alive while we exec into it.
	output.Infof("Creating build container ...")
	buildID, err := b.cl.Run(ctx, client.RunOptions{
		Image:     baseImage,
		Args:      []string{"cmd.exe", "/c", "ping", "-t", "127.0.0.1"},
		Detach:    true,
		Env:       envVars,
		Isolation: client.IsolationHyperV,
	})
	if err != nil {
		return fmt.Errorf("cannot start build container: %w", err)
	}
	defer func() { _ = b.cl.RemoveContainer(ctx, buildID, true) }()

	// Step 3 — execute COPY, RUN, WORKDIR, ARG, EXPOSE instructions in order.
	step := 2
	for _, instr := range bf.Instructions[1:] {
		switch instr.Type {
		case InstrARG:
			raw := instr.Args[0]
			// ARG can be NAME=default or just NAME.
			if strings.Contains(raw, "=") {
				parts := strings.SplitN(raw, "=", 2)
				// Only set default if not already overridden by buildArgs.
				if _, exists := state.args[parts[0]]; !exists {
					state.args[parts[0]] = parts[1]
				}
			}
			// If just NAME with no default and not in buildArgs, nothing to do.

		case InstrWORKDIR:
			dir := substituteArgs(instr.Args[0], state.args)
			state.workDir = dir
			output.Infof("Step %d/N: WORKDIR %s", step, dir)
			step++

		case InstrEXPOSE:
			for _, port := range instr.Args {
				port = substituteArgs(port, state.args)
				state.exposed = append(state.exposed, port)
			}
			output.Infof("Step %d/N: EXPOSE %s", step, strings.Join(instr.Args, " "))
			step++

		case InstrCOPY:
			src := filepath.Join(contextDir, substituteArgs(instr.Args[0], state.args))
			dst := substituteArgs(instr.Args[1], state.args)
			output.Infof("Step %d/N: COPY %s → %s", step, instr.Args[0], dst)
			if err := b.execCopy(ctx, buildID, src, dst); err != nil {
				return fmt.Errorf("COPY %s %s: %w", instr.Args[0], dst, err)
			}
			step++

		case InstrRUN:
			cmd := substituteArgs(instr.Args[0], state.args)
			output.Infof("Step %d/N: RUN %s", step, cmd)
			if err := b.execRun(ctx, buildID, cmd, state.workDir); err != nil {
				return fmt.Errorf("RUN %s: %w", cmd, err)
			}
			step++
		}
	}

	// Step 4 — stop the build container before committing.
	output.Infof("Committing image as %s ...", outputRef)
	if err := b.cl.StopContainer(ctx, buildID); err != nil {
		return fmt.Errorf("cannot stop build container: %w", err)
	}

	if err := b.cl.CommitContainer(ctx, buildID, outputRef, client.CommitOptions{
		OverrideCmd:  overrideCmd,
		WorkingDir:   state.workDir,
		ExposedPorts: state.exposed,
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	output.Successf("Successfully built %s", outputRef)
	return nil
}

// execCopy copies the contents of the host directory src into the container at dst.
// It starts a temporary HTTP server, then execs PowerShell in the container to
// download and extract the archive — the only reliable COPY mechanism for
// Hyper-V isolated containers (bind mounts are not supported).
func (b *Builder) execCopy(ctx context.Context, containerID, src, dst string) error {
	srv, port, err := newFileServer(src)
	if err != nil {
		return err
	}
	defer srv.Close()

	gatewayIP, err := network.GatewayIP()
	if err != nil {
		return fmt.Errorf("cannot determine host IP: %w", err)
	}

	winDst := toWindowsPath(dst)
	url := fmt.Sprintf("http://%s:%d/archive.zip", gatewayIP, port)

	psCmd := fmt.Sprintf(
		"New-Item -ItemType Directory -Force '%s' | Out-Null; "+
			"Invoke-WebRequest -Uri '%s' -OutFile C:\\__barge_copy.zip; "+
			"Expand-Archive -Path C:\\__barge_copy.zip -DestinationPath '%s' -Force; "+
			"Remove-Item C:\\__barge_copy.zip -Force",
		winDst, url, winDst,
	)

	return b.cl.Exec(ctx, client.ExecOptions{
		ContainerID: containerID,
		Args:        []string{"powershell.exe", "-NonInteractive", "-Command", psCmd},
	})
}

// execRun runs a shell command inside the build container via cmd.exe.
// When workDir is non-empty the command is prefixed with a cd to that directory.
func (b *Builder) execRun(ctx context.Context, containerID, command, workDir string) error {
	cmd := command
	if workDir != "" {
		winDir := toWindowsPath(workDir)
		cmd = fmt.Sprintf("cd /d %s && %s", winDir, command)
	}
	return b.cl.Exec(ctx, client.ExecOptions{
		ContainerID: containerID,
		Args:        []string{"cmd.exe", "/s", "/c", cmd},
	})
}

// toWindowsPath converts a path like /app or /app/sub to C:\app\sub.
func toWindowsPath(path string) string {
	if strings.HasPrefix(path, "/") {
		path = "C:" + path
	}
	return strings.ReplaceAll(path, "/", "\\")
}
