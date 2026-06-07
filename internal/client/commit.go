package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/asbassan/barge/internal/output"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	ctrdiff "github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/snapshots"
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

	// Lease pins all content written during this operation so containerd's GC
	// cannot remove it before the image record is created.
	ctx, done, err := cl.c.WithLease(ctx)
	if err != nil {
		return fmt.Errorf("cannot create content lease: %w", err)
	}
	defer done(ctx) //nolint:errcheck

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

	// Stat the container snapshot to find its parent (top image layer).
	snapInfo, err := snapshotter.Stat(ctx, containerID)
	if err != nil {
		return fmt.Errorf("cannot stat container snapshot: %w", err)
	}

	upper, err := snapshotter.Mounts(ctx, containerID)
	if err != nil {
		return fmt.Errorf("cannot get container mounts: %w", err)
	}

	// Committed image-layer snapshots cannot be mounted via Mounts() on Windows
	// (only active/view snapshots are mountable). Create a temporary read-only
	// view snapshot of the parent — view snapshots ARE mountable and return
	// exactly 1 mount, which the Windows diff plugin requires.
	var lower []mount.Mount
	if snapInfo.Parent != "" {
		viewKey := containerID + "-lower-view"
		lower, err = snapshotter.View(ctx, viewKey, snapInfo.Parent,
			snapshots.WithLabels(map[string]string{"containerd.io/gc.root": "barge-commit"}))
		if err != nil {
			return fmt.Errorf("cannot create view of parent snapshot: %w", err)
		}
		defer snapshotter.Remove(ctx, viewKey) //nolint:errcheck
	}

	// Compute the diff layer (writes result to the content store).
	layerDesc, err := cl.c.DiffService().Compare(ctx, lower, upper,
		ctrdiff.WithMediaType(ocispec.MediaTypeImageLayerGzip),
		ctrdiff.WithReference(newRef+"-layer"),
	)
	if err != nil {
		return fmt.Errorf("cannot compute diff layer: %w", err)
	}

	// Verify the layer content was actually stored — the Windows diff plugin
	// may return a valid descriptor without persisting the blob.
	if _, err := cs.Info(ctx, layerDesc.Digest); err != nil {
		return fmt.Errorf("diff layer not stored in content store (digest %s): %w", layerDesc.Digest, err)
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

	// DiffID must be the SHA256 of the *uncompressed* layer tar.
	// The Windows diff plugin often omits the "containerd.io/uncompressed" annotation,
	// so we fall back to decompressing the blob ourselves.
	var newDiffID digest.Digest
	if id, ok := layerDesc.Annotations["containerd.io/uncompressed"]; ok {
		if parsed, err := digest.Parse(id); err == nil {
			newDiffID = parsed
		}
	}
	if newDiffID == "" {
		computed, err := uncompressedDigest(ctx, cs, layerDesc)
		if err != nil {
			return fmt.Errorf("cannot compute diff ID: %w", err)
		}
		newDiffID = computed
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

	// Pin all three blobs with a permanent GC root label so containerd's GC
	// cannot evict them between CommitContainer returning and barge run reading them.
	gcLabel := map[string]string{"containerd.io/gc.root": "barge"}
	for _, d := range []digest.Digest{layerDesc.Digest, configDesc.Digest, manifestDesc.Digest} {
		if _, err := cs.Update(ctx, content.Info{Digest: d, Labels: gcLabel},
			"labels.containerd.io/gc.root"); err != nil {
			output.Warnf("cannot pin blob %s: %v", d, err) // non-fatal
		}
	}

	output.Infof("  layer:    %s", layerDesc.Digest)
	output.Infof("  config:   %s", configDesc.Digest)
	output.Infof("  manifest: %s", manifestDesc.Digest)

	// Register the new image using Create; if one already exists (stale build),
	// update it atomically. This avoids a Delete→Create window where GC could
	// run with no image record anchoring the newly written blobs.
	imgSvc := cl.c.ImageService()
	newImgRecord, err := imgSvc.Create(ctx, images.Image{
		Name:   newRef,
		Target: manifestDesc,
	})
	if errdefs.IsAlreadyExists(err) {
		newImgRecord, err = imgSvc.Update(ctx, images.Image{
			Name:   newRef,
			Target: manifestDesc,
		})
	}
	if err != nil {
		return fmt.Errorf("cannot create image record for %q: %w", newRef, err)
	}

	// Unpack the new image so its layers exist as VHD snapshots. Without this
	// step, WithNewSnapshot cannot create a container because the chain ID of
	// the new top layer has no corresponding snapshot entry yet.
	newImg := containerd.NewImage(cl.c, newImgRecord)
	if err := newImg.Unpack(ctx, windowsSnapshotter); err != nil {
		return fmt.Errorf("cannot unpack new image: %w", err)
	}

	return nil
}

// uncompressedDigest reads a gzip-compressed blob from the content store and
// returns the SHA256 digest of its uncompressed content (the OCI DiffID).
func uncompressedDigest(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (digest.Digest, error) {
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return "", err
	}
	defer ra.Close()

	gr, err := gzip.NewReader(io.NewSectionReader(ra, 0, desc.Size))
	if err != nil {
		return "", err
	}
	defer gr.Close()

	digester := digest.SHA256.Digester()
	if _, err := io.Copy(digester.Hash(), gr); err != nil {
		return "", err
	}
	return digester.Digest(), nil
}
