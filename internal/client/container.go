package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/asbassan/barge/internal/network"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/errdefs"
)

// Status represents the lifecycle state of a container.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusCreated Status = "created"
	StatusPaused  Status = "paused"
)

// ContainerInfo holds the display fields for barge ps.
type ContainerInfo struct {
	ID        string
	Name      string
	Image     string
	Status    Status
	CreatedAt time.Time
	Ports     string
}

// ListContainers returns all containers in the barge namespace.
// Pass showAll=true to include stopped containers.
func (cl *Client) ListContainers(ctx context.Context, showAll bool) ([]ContainerInfo, error) {
	ctx = cl.ctx(ctx)

	containers, err := cl.c.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	result := make([]ContainerInfo, 0, len(containers))
	for _, c := range containers {
		info, err := c.Info(ctx)
		if err != nil {
			continue
		}

		status := StatusCreated
		task, err := c.Task(ctx, nil)
		if err == nil {
			s, err := task.Status(ctx)
			if err == nil {
				status = Status(s.Status)
			}
		} else if errdefs.IsNotFound(err) {
			status = StatusStopped
		}

		if !showAll && status == StatusStopped {
			continue
		}

		result = append(result, ContainerInfo{
			ID:        c.ID(),
			Name:      c.ID(),
			Image:     info.Image,
			Status:    status,
			CreatedAt: info.CreatedAt,
		})
	}
	return result, nil
}

// StopContainer sends a stop signal to a running container and cleans up its task.
func (cl *Client) StopContainer(ctx context.Context, id string) error {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, id)
	if err != nil {
		return containerNotFound(id)
	}

	task, err := c.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return fmt.Errorf("container %q is not running", id)
		}
		return fmt.Errorf("cannot get task for container %q: %w", id, err)
	}

	// If the process already exited, just delete the task record and return.
	s, err := task.Status(ctx)
	if err == nil && s.Status != containerd.Running {
		_, _ = task.Delete(ctx)
		return nil
	}

	exitCh, err := task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("cannot wait for container %q: %w", id, err)
	}

	// Windows containers don't use POSIX signals; Kill sends TerminateProcess.
	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		if killErr := task.Kill(ctx, syscall.SIGKILL); killErr != nil {
			// Process exited between our status check and the kill — that's fine.
			_, _ = task.Delete(ctx)
			return nil
		}
	}

	select {
	case <-exitCh:
	case <-time.After(10 * time.Second):
		_ = task.Kill(ctx, syscall.SIGKILL)
		<-exitCh
	}

	if _, err := task.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("cannot delete task for %q: %w", id, err)
	}
	return nil
}

// RemoveContainer deletes a stopped container and its snapshot.
func (cl *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, id)
	if err != nil {
		return containerNotFound(id)
	}

	// Always attempt to clean up the task record first, whether force or not.
	// A task can exist with an already-exited process (e.g. a detached container
	// whose process finished before the user ran barge rm).
	task, taskErr := c.Task(ctx, nil)
	if taskErr == nil {
		s, _ := task.Status(ctx)
		isRunning := s.Status == containerd.Running

		if isRunning && !force {
			return fmt.Errorf(
				"container %q is still running — stop it first with: barge stop %s\n"+
					"  Or force remove with: barge rm -f %s",
				id, id, id,
			)
		}

		if isRunning {
			_ = task.Kill(ctx, syscall.SIGKILL)
			exitCh, _ := task.Wait(ctx)
			select {
			case <-exitCh:
			case <-time.After(5 * time.Second):
			}
		}
		_, _ = task.Delete(ctx)
	}

	// Delete the HCN endpoint stored in the container's label.
	if info, err := c.Info(ctx); err == nil {
		if epID := info.Labels[labelEndpoint]; epID != "" {
			network.DeleteEndpoint(epID)
		}
	}

	if err := c.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return fmt.Errorf("failed to remove container %q: %w", id, err)
	}
	return nil
}

// Logs reads the log file for a detached container.
// Without follow it copies the file to stdout once; with follow it polls for new content.
func (cl *Client) Logs(ctx context.Context, id string, follow bool) error {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, id)
	if err != nil {
		return containerNotFound(id)
	}

	info, err := c.Info(ctx)
	if err != nil {
		return fmt.Errorf("cannot read container info: %w", err)
	}

	logPath, ok := info.Labels[labelLogFile]
	if !ok || logPath == "" {
		return fmt.Errorf(
			"no log file for container %q\n\n"+
				"  Logs are only captured for containers started with -d (detach).\n"+
				"  Foreground containers write directly to your terminal.",
			id,
		)
	}

	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("cannot open log file: %w", err)
	}
	defer f.Close()

	if !follow {
		_, err = io.Copy(os.Stdout, f)
		return err
	}

	// Follow mode: poll every 500ms, seeking to the last position.
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		n, err := io.Copy(os.Stdout, f)
		offset += n
		if err != nil && err != io.EOF {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ExecOptions configures barge exec.
type ExecOptions struct {
	ContainerID string
	Args        []string
	Interactive bool
}

// Exec runs a command inside a running container.
func (cl *Client) Exec(ctx context.Context, opts ExecOptions) error {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, opts.ContainerID)
	if err != nil {
		return containerNotFound(opts.ContainerID)
	}

	task, err := c.Task(ctx, nil)
	if err != nil {
		return fmt.Errorf("container %q is not running", opts.ContainerID)
	}

	execID := fmt.Sprintf("exec-%d", time.Now().UnixNano())

	spec, err := c.Spec(ctx)
	if err != nil {
		return fmt.Errorf("cannot read container spec: %w", err)
	}

	pspec := *spec.Process
	pspec.Args = opts.Args
	pspec.Terminal = opts.Interactive

	// Always attach host stdio so command output is visible.
	// pspec.Terminal controls PTY allocation; the IO creator is the same either way.
	ioCreator := cio.NewCreator(cio.WithStdio)

	proc, err := task.Exec(ctx, execID, &pspec, ioCreator)
	if err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	exitCh, err := proc.Wait(ctx)
	if err != nil {
		return fmt.Errorf("cannot wait for exec process: %w", err)
	}

	if err := proc.Start(ctx); err != nil {
		return fmt.Errorf("cannot start exec process: %w", err)
	}

	exitStatus := <-exitCh
	if _, delErr := proc.Delete(ctx); delErr != nil {
		// Non-fatal.
	}
	return exitStatus.Error()
}

// Inspect returns the raw JSON metadata for a container or image.
func (cl *Client) InspectContainer(ctx context.Context, id string) (string, error) {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, id)
	if err != nil {
		return "", containerNotFound(id)
	}

	info, err := c.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot inspect container %q: %w", id, err)
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// InspectImage returns the raw JSON metadata for a local image.
func (cl *Client) InspectImage(ctx context.Context, ref string) (string, error) {
	ctx = cl.ctx(ctx)

	img, err := cl.GetImage(ctx, ref)
	if err != nil {
		return "", err
	}

	info := map[string]any{
		"name":      img.Name(),
		"target":    img.Target(),
		"metadata":  img.Metadata(),
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func containerNotFound(id string) error {
	return fmt.Errorf(
		"container %q not found\n\n  List all containers with: barge ps -a",
		id,
	)
}
