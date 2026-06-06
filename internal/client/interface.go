package client

import (
	"context"

	"github.com/containerd/containerd"
)

// Runtime is the interface that all BARGE container operations go through.
// main.go and command handlers depend on this, not on *Client directly,
// so commands are independently testable by substituting a fake.
type Runtime interface {
	// Image operations
	Pull(ctx context.Context, ref string) (containerd.Image, error)
	ListImages(ctx context.Context) ([]ImageInfo, error)
	RemoveImage(ctx context.Context, ref string) error
	GetImage(ctx context.Context, ref string) (containerd.Image, error)
	InspectImage(ctx context.Context, ref string) (string, error)
	TagImage(ctx context.Context, src, dst string) error
	PushImage(ctx context.Context, ref string) error

	// Container lifecycle
	Run(ctx context.Context, opts RunOptions) (string, error)
	ListContainers(ctx context.Context, showAll bool) ([]ContainerInfo, error)
	StopContainer(ctx context.Context, id string) error
	RemoveContainer(ctx context.Context, id string, force bool) error
	CommitContainer(ctx context.Context, containerID, newRef string, opts CommitOptions) error
	Exec(ctx context.Context, opts ExecOptions) error
	Logs(ctx context.Context, id string, follow bool) error
	InspectContainer(ctx context.Context, id string) (string, error)
	Stats(ctx context.Context, id string) error

	// Credential management
	Login(ctx context.Context, registry, username, password string) error
	Logout(ctx context.Context, registry string) error

	// Daemon info
	Version(ctx context.Context) (string, error)

	Close() error
}

// Verify at compile time that *Client satisfies Runtime.
var _ Runtime = (*Client)(nil)
