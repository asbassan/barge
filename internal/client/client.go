// Package client wraps the containerd Go client with BARGE-specific defaults:
// Hyper-V isolation, the "barge" namespace, and beginner-friendly error messages.
package client

import (
	"context"
	"fmt"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/asbassan/barge/internal/preflight"
)

const (
	bargeNamespace = "barge"
	hyperVRuntime  = "io.containerd.runhcs.v1"
	windowsSnapshotter = "windows"
)

// Client wraps the containerd client with BARGE defaults.
type Client struct {
	c *containerd.Client
}

// New connects to the local containerd daemon.
func New() (*Client, error) {
	c, err := containerd.New(preflight.ContainerdAddress())
	if err != nil {
		return nil, fmt.Errorf(
			"cannot connect to containerd at %s\n\n"+
				"  Make sure containerd is running:\n"+
				"    Start-Service containerd\n\n"+
				"  Original error: %w",
			preflight.ContainerdAddress(), err,
		)
	}
	return &Client{c: c}, nil
}

// Close releases the underlying connection.
func (cl *Client) Close() error {
	return cl.c.Close()
}

// ctx returns a context scoped to the barge namespace.
func (cl *Client) ctx(parent context.Context) context.Context {
	return namespaces.WithNamespace(parent, bargeNamespace)
}

// Version returns the containerd server version string.
func (cl *Client) Version(ctx context.Context) (string, error) {
	v, err := cl.c.Version(cl.ctx(ctx))
	if err != nil {
		return "", err
	}
	return v.Version, nil
}
