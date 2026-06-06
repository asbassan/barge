package client

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/asbassan/barge/internal/network"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	labelEndpoint = "barge.endpoint"
	labelLogFile  = "barge.logfile"
)

// containerLogPath returns the path to a container's log file.
func containerLogPath(id string) string {
	return filepath.Join(os.Getenv("ProgramData"), "barge", "logs", id+".log")
}

// Isolation selects the Windows container isolation mode.
type Isolation string

const (
	IsolationHyperV   Isolation = "hyperv"
	IsolationProcess  Isolation = "process"
)

// RunOptions configures a barge run invocation.
type RunOptions struct {
	Image     string
	Name      string
	Args      []string  // override the image's default CMD
	Detach    bool      // -d: run in background
	Remove    bool      // --rm: delete container on exit
	Env       []string  // -e KEY=VALUE
	Volumes   []string  // -v host:container
	Ports     []string  // -p hostPort:containerPort[/proto]
	Isolation Isolation // --isolation hyperv|process (default: hyperv)
}

// Run creates and starts a Hyper-V isolated Windows container.
func (cl *Client) Run(ctx context.Context, opts RunOptions) (id string, err error) {
	ctx = cl.ctx(ctx)

	img, err := cl.GetImage(ctx, opts.Image)
	if err != nil {
		return "", err
	}

	id = opts.Name
	if id == "" {
		id = randomName()
	}

	// Set up HCN NAT network and create an endpoint for this container.
	networkID, err := network.EnsureNATNetwork()
	if err != nil {
		return "", fmt.Errorf("network setup: %w", err)
	}

	portMappings, err := parsePortMappings(opts.Ports)
	if err != nil {
		return "", err
	}

	endpointID, err := network.CreateEndpoint(networkID, id, portMappings)
	if err != nil {
		return "", err
	}
	// Clean up the endpoint if anything below fails before the container is stored.
	defer func() {
		if err != nil {
			network.DeleteEndpoint(endpointID)
		}
	}()

	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(img),
		withNetworkEndpoint(endpointID),
	}
	if opts.Isolation != IsolationProcess {
		specOpts = append(specOpts, withHyperVIsolation())
	}
	if len(opts.Args) > 0 {
		specOpts = append(specOpts, withArgs(opts.Args))
	}
	if len(opts.Env) > 0 {
		specOpts = append(specOpts, oci.WithEnv(opts.Env))
	}
	if len(opts.Volumes) > 0 {
		mounts, err := parseMappedDirectories(opts.Volumes)
		if err != nil {
			return "", err
		}
		specOpts = append(specOpts, withMappedDirectories(mounts))
	}

	labels := map[string]string{
		labelEndpoint: endpointID,
	}

	var ioCreator cio.Creator
	if opts.Detach {
		logPath := containerLogPath(id)
		if mkErr := os.MkdirAll(filepath.Dir(logPath), 0755); mkErr != nil {
			return "", fmt.Errorf("cannot create log directory: %w", mkErr)
		}
		labels[labelLogFile] = logPath
		ioCreator = cio.LogFile(logPath)
	} else {
		ioCreator = cio.NewCreator(cio.WithStdio)
	}

	container, err := cl.c.NewContainer(ctx, id,
		containerd.WithImage(img),
		containerd.WithNewSnapshot(id, img),
		containerd.WithNewSpec(specOpts...),
		containerd.WithRuntime(hyperVRuntime, nil),
		containerd.WithAdditionalContainerLabels(labels),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container %q: %w", id, err)
	}

	task, err := container.NewTask(ctx, ioCreator)
	if err != nil {
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return "", fmt.Errorf("failed to create task: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	if opts.Detach {
		// Endpoint persists; it's deleted when the container is removed.
		err = nil
		return id, nil
	}

	// Foreground: wait for exit.
	exitCh, err := task.Wait(ctx)
	if err != nil {
		return id, fmt.Errorf("failed to wait for container: %w", err)
	}
	exitStatus := <-exitCh

	if _, delErr := task.Delete(ctx); delErr != nil {
		// Non-fatal.
	}
	if opts.Remove {
		network.DeleteEndpoint(endpointID)
		if delErr := container.Delete(ctx, containerd.WithSnapshotCleanup); delErr != nil {
			fmt.Printf("warning: could not remove container %s: %v\n", id, delErr)
		}
	}

	if exitStatus.Error() != nil {
		err = exitStatus.Error()
		return id, err
	}
	err = nil
	return id, nil
}

// withHyperVIsolation sets the Hyper-V isolation flag in the OCI runtime spec.
// runhcs starts the container inside a lightweight Hyper-V VM so each container
// gets its own Windows kernel instance.
func withHyperVIsolation() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Windows == nil {
			s.Windows = &specs.Windows{}
		}
		s.Windows.HyperV = &specs.WindowsHyperV{}
		return nil
	}
}

// withNetworkEndpoint attaches an HCN endpoint to the container's network namespace.
func withNetworkEndpoint(endpointID string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Windows == nil {
			s.Windows = &specs.Windows{}
		}
		if s.Windows.Network == nil {
			s.Windows.Network = &specs.WindowsNetwork{}
		}
		s.Windows.Network.EndpointList = []string{endpointID}
		s.Windows.Network.AllowUnqualifiedDNSQuery = true
		return nil
	}
}

func withArgs(args []string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Process.Args = args
		return nil
	}
}

// mappedDir is a host→container directory binding.
type mappedDir struct {
	Host      string
	Container string
	Readonly  bool
}

// parseMappedDirectories parses "-v host:container[:ro]" specs.
// Handles Windows drive letters (e.g. C:\host:C:\container) by treating
// a single letter followed by a colon+backslash as part of the path, not a separator.
func parseMappedDirectories(volumes []string) ([]mappedDir, error) {
	result := make([]mappedDir, 0, len(volumes))
	for _, v := range volumes {
		parts := splitWindowsVolume(v)
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("invalid volume %q: expected host:container[:ro]", v)
		}
		readonly := len(parts) == 3 && parts[2] == "ro"
		if len(parts) == 3 && !readonly {
			return nil, fmt.Errorf("invalid volume option %q in %q: only :ro is supported", parts[2], v)
		}
		result = append(result, mappedDir{Host: parts[0], Container: parts[1], Readonly: readonly})
	}
	return result, nil
}

// splitWindowsVolume splits "C:\host:C:\container[:ro]" on colons that are not
// Windows drive letters. A drive letter is a single ASCII letter immediately
// followed by ":" and then "\" or "/".
func splitWindowsVolume(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != ':' {
			continue
		}
		// Skip drive letter: single letter at start of current segment + colon + slash.
		segLen := i - start
		if segLen == 1 && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '/') {
			continue
		}
		parts = append(parts, s[start:i])
		start = i + 1
	}
	parts = append(parts, s[start:])
	return parts
}

func withMappedDirectories(dirs []mappedDir) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		for _, d := range dirs {
			var opts []string
			if d.Readonly {
				opts = []string{"ro"}
			}
			s.Mounts = append(s.Mounts, specs.Mount{
				Source:      d.Host,
				Destination: d.Container,
				Options:     opts,
			})
		}
		return nil
	}
}

func parsePortMappings(specs []string) ([]network.PortMapping, error) {
	result := make([]network.PortMapping, 0, len(specs))
	for _, s := range specs {
		pm, err := network.ParsePortMapping(s)
		if err != nil {
			return nil, err
		}
		result = append(result, pm)
	}
	return result, nil
}

var adjectives = []string{
	"brave", "calm", "eager", "kind", "swift", "bold", "cool", "dark",
	"fast", "glad", "huge", "iron", "keen", "lean", "mild", "neat",
}
var nouns = []string{
	"barge", "crane", "dock", "ferry", "harbor", "jetty", "keel", "mast",
	"pier", "raft", "ship", "stern", "tug", "vessel", "wake", "yard",
}

func randomName() string {
	return strings.Join([]string{
		adjectives[rand.Intn(len(adjectives))],
		nouns[rand.Intn(len(nouns))],
	}, "_")
}
