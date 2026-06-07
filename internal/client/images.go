package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/asbassan/barge/internal/output"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	dockerresolver "github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/remotes"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ImageInfo holds the display fields for barge images.
type ImageInfo struct {
	Name      string
	Tag       string
	Digest    string
	Size      int64
	CreatedAt time.Time
}

// normalizeRef expands short Docker Hub references to fully-qualified form
// so containerd's URL parser doesn't mistake the image name for a hostname.
// "python:3.11-slim"           → "docker.io/library/python:3.11-slim"
// "myorg/myapp:latest"         → "docker.io/myorg/myapp:latest"
// "mcr.microsoft.com/win:ltsc" → unchanged (already has a registry)
func normalizeRef(ref string) string {
	slash := strings.IndexByte(ref, '/')
	if slash == -1 {
		// No slash: official Docker Hub image ("ubuntu:22.04", "python:3.11-slim")
		return "docker.io/library/" + ref
	}
	// Has a slash — check if the prefix before it looks like a registry hostname
	// (contains a dot or colon, or is "localhost").
	host := ref[:slash]
	if strings.ContainsAny(host, ".:") || host == "localhost" {
		return ref // already has a registry prefix
	}
	// User-scoped Docker Hub image: "myorg/myapp:tag"
	return "docker.io/" + ref
}

// newResolver builds a docker resolver that injects stored credentials.
func newResolver() remotes.Resolver {
	return dockerresolver.NewResolver(dockerresolver.ResolverOptions{
		Credentials: credentialsForHost,
	})
}

// Pull downloads a Windows container image from a registry.
// It selects the image variant matching the host Windows build (containerd handles this).
func (cl *Client) Pull(ctx context.Context, ref string) (containerd.Image, error) {
	ctx = cl.ctx(ctx)
	ref = normalizeRef(ref)

	// Windows platform — containerd picks the correct os.version for the host.
	windowsPlatform := ocispec.Platform{
		OS:           "windows",
		Architecture: "amd64",
	}

	output.Infof("Pulling %s ...", ref)

	// Print elapsed time every 10s so the user knows a large image is still downloading.
	pullDone := make(chan struct{})
	go func() {
		start := time.Now()
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-pullDone:
				return
			case <-tick.C:
				output.Infof("  still pulling... (%s elapsed — large Windows images can be 5-6 GB)", time.Since(start).Round(time.Second))
			}
		}
	}()

	img, err := cl.c.Pull(ctx, ref,
		containerd.WithPlatformMatcher(platforms.Only(windowsPlatform)),
		containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(windowsSnapshotter),
		containerd.WithResolver(newResolver()),
	)
	close(pullDone)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to pull %q\n\n"+
				"  Check that:\n"+
				"  • The image name and tag are correct\n"+
				"  • You have an internet connection\n"+
				"  • For private registries run: barge login <registry>\n\n"+
				"  Original error: %w",
			ref, err,
		)
	}
	return img, nil
}

// ListImages returns all images stored in the barge namespace.
func (cl *Client) ListImages(ctx context.Context) ([]ImageInfo, error) {
	ctx = cl.ctx(ctx)

	imgs, err := cl.c.ListImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	result := make([]ImageInfo, 0, len(imgs))
	for _, img := range imgs {
		size, _ := img.Size(ctx)
		result = append(result, ImageInfo{
			Name:      img.Name(),
			Digest:    img.Target().Digest.String(),
			Size:      size,
			CreatedAt: img.Metadata().CreatedAt,
		})
	}
	return result, nil
}

// RemoveImage deletes a local image.
func (cl *Client) RemoveImage(ctx context.Context, ref string) error {
	ctx = cl.ctx(ctx)

	if err := cl.c.ImageService().Delete(ctx, ref); err != nil {
		return fmt.Errorf("failed to remove image %q: %w", ref, err)
	}
	return nil
}

// GetImage returns a single image by reference, or a helpful error.
func (cl *Client) GetImage(ctx context.Context, ref string) (containerd.Image, error) {
	ctx = cl.ctx(ctx)

	// Try normalized form first (pulled images are stored as docker.io/...).
	normalized := normalizeRef(ref)
	if img, err := cl.c.GetImage(ctx, normalized); err == nil {
		return img, nil
	}

	// Fall back to the original ref — locally built images are stored under
	// whatever name the user gave them (e.g. "tessa1:v1", not "docker.io/...").
	if normalized != ref {
		if img, err := cl.c.GetImage(ctx, ref); err == nil {
			return img, nil
		}
	}

	return nil, fmt.Errorf(
		"image %q not found locally\n\n  Pull it first:\n    barge pull %s",
		ref, ref,
	)
}

// TagImage creates a new image record with name dst pointing to the same
// manifest descriptor as src.
func (cl *Client) TagImage(ctx context.Context, src, dst string) error {
	ctx = cl.ctx(ctx)

	img, err := cl.GetImage(ctx, src)
	if err != nil {
		return err
	}

	if _, err := cl.c.ImageService().Create(ctx, images.Image{
		Name:   dst,
		Target: img.Target(),
	}); err != nil {
		return fmt.Errorf("failed to tag image %q as %q: %w", src, dst, err)
	}
	return nil
}

// PushImage pushes a local image to its registry using stored credentials.
func (cl *Client) PushImage(ctx context.Context, ref string) error {
	ctx = cl.ctx(ctx)

	img, err := cl.GetImage(ctx, ref)
	if err != nil {
		return err
	}

	fmt.Printf("Pushing %s ...\n", ref)

	if err := cl.c.Push(ctx, ref, img.Target(),
		containerd.WithResolver(newResolver()),
	); err != nil {
		return fmt.Errorf("failed to push %q: %w", ref, err)
	}
	return nil
}
