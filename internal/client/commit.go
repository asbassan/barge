package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/containerd/containerd/content"
	ctrdiff "github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// CommitOptions controls what gets baked into the new image config.
type CommitOptions struct {
	OverrideCmd  []string
	WorkingDir   string
	ExposedPorts []string
}

// CommitContainer creates a new image from a stopped container's current filesystem.
func (cl *Client) CommitContainer(ctx context.Context, containerID, newRef string, opts CommitOptions) error {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, containerID)
	if err != nil {
		return containerNotFound(containerID)
	}

	info, err := c.Info(ctx)
	if err != nil {
		return fmt.Errorf("cannot read container info: %w", err)
	}

	baseImg, err := cl.c.GetImage(ctx, info.Image)
	if err != nil {
		return fmt.Errorf("cannot load base image %q: %w", info.Image, err)
	}

	spec, err := c.Spec(ctx)
	if err != nil {
		return fmt.Errorf("cannot read container spec: %w", err)
	}

	cs := cl.c.ContentStore()
	snapshotter := cl.c.SnapshotService(windowsSnapshotter)

	// Get the snapshot layer stack for the container.
	snapInfo, err := snapshotter.Stat(ctx, containerID)
	if err != nil {
		return fmt.Errorf("cannot stat snapshot %q: %w", containerID, err)
	}

	upper, err := snapshotter.Mounts(ctx, containerID)
	if err != nil {
		return fmt.Errorf("cannot get container mounts: %w", err)
	}

	var lower []mount.Mount
	if snapInfo.Parent != "" {
		lower, err = snapshotter.Mounts(ctx, snapInfo.Parent)
		if err != nil {
			return fmt.Errorf("cannot get parent snapshot mounts: %w", err)
		}
	}

	// Compute the diff layer (writes result to the content store).
	layerDesc, err := cl.c.DiffService().Compare(ctx, lower, upper,
		ctrdiff.WithMediaType(ocispec.MediaTypeImageLayerGzip),
		ctrdiff.WithReference(newRef+"-layer"),
	)
	if err != nil {
		return fmt.Errorf("cannot compute diff layer: %w", err)
	}

	// Resolve the base image manifest for the Windows platform.
	windowsPlatform := ocispec.Platform{OS: "windows", Architecture: "amd64"}
	baseManifest, err := images.Manifest(ctx, cs, baseImg.Target(), platforms.Only(windowsPlatform))
	if err != nil {
		return fmt.Errorf("cannot read base manifest: %w", err)
	}

	// Load and update the base image config.
	configBytes, err := content.ReadBlob(ctx, cs, baseManifest.Config)
	if err != nil {
		return fmt.Errorf("cannot read base config: %w", err)
	}

	var imgConfig ocispec.Image
	if err := json.Unmarshal(configBytes, &imgConfig); err != nil {
		return fmt.Errorf("cannot parse base config: %w", err)
	}

	imgConfig.Config.Env = spec.Process.Env
	if len(opts.OverrideCmd) > 0 {
		imgConfig.Config.Cmd = opts.OverrideCmd
	} else {
		imgConfig.Config.Cmd = spec.Process.Args
	}
	if opts.WorkingDir != "" {
		imgConfig.Config.WorkingDir = opts.WorkingDir
	}
	if len(opts.ExposedPorts) > 0 {
		if imgConfig.Config.ExposedPorts == nil {
			imgConfig.Config.ExposedPorts = make(map[string]struct{})
		}
		for _, port := range opts.ExposedPorts {
			imgConfig.Config.ExposedPorts[port] = struct{}{}
		}
	}

	// Append the new layer's DiffID (uncompressed digest of the layer tar).
	newDiffID := layerDesc.Digest
	if id, ok := layerDesc.Annotations["containerd.io/uncompressed"]; ok {
		if parsed, err := digest.Parse(id); err == nil {
			newDiffID = parsed
		}
	}
	imgConfig.RootFS.DiffIDs = append(imgConfig.RootFS.DiffIDs, newDiffID)

	// Store the new image config.
	newConfigBytes, err := json.Marshal(imgConfig)
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}
	configDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(newConfigBytes),
		Size:      int64(len(newConfigBytes)),
	}
	if err := content.WriteBlob(ctx, cs, newRef+"-config",
		bytes.NewReader(newConfigBytes), configDesc); err != nil {
		return fmt.Errorf("cannot store config: %w", err)
	}

	// Build and store the new manifest.
	newManifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    append(baseManifest.Layers, layerDesc),
	}
	manifestBytes, err := json.Marshal(newManifest)
	if err != nil {
		return fmt.Errorf("cannot marshal manifest: %w", err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	if err := content.WriteBlob(ctx, cs, newRef,
		bytes.NewReader(manifestBytes), manifestDesc); err != nil {
		return fmt.Errorf("cannot store manifest: %w", err)
	}

	// Register the new image.
	if _, err := cl.c.ImageService().Create(ctx, images.Image{
		Name:   newRef,
		Target: manifestDesc,
	}); err != nil {
		return fmt.Errorf("cannot create image record for %q: %w", newRef, err)
	}

	return nil
}
