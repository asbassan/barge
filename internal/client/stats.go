package client

import (
	"context"
	"fmt"

	"github.com/asbassan/barge/internal/output"
	"github.com/containerd/containerd/errdefs"
)

// Stats displays resource usage statistics for a container.
func (cl *Client) Stats(ctx context.Context, id string) error {
	ctx = cl.ctx(ctx)

	c, err := cl.c.LoadContainer(ctx, id)
	if err != nil {
		return containerNotFound(id)
	}

	info, err := c.Info(ctx)
	if err != nil {
		return fmt.Errorf("cannot read container info: %w", err)
	}

	task, err := c.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			output.PrintTable(
				[]string{"CONTAINER", "IMAGE", "STATUS"},
				[][]string{{id, info.Image, "stopped"}},
			)
			return nil
		}
		return fmt.Errorf("cannot get task for container %q: %w", id, err)
	}

	s, err := task.Status(ctx)
	if err != nil {
		return fmt.Errorf("cannot get task status: %w", err)
	}
	status := string(s.Status)

	metric, err := task.Metrics(ctx)
	if err != nil {
		// Metrics not available — show status only.
		output.PrintTable(
			[]string{"CONTAINER", "IMAGE", "STATUS"},
			[][]string{{id, info.Image, status}},
		)
		return nil
	}

	var sampled string
	if metric.Timestamp != nil {
		sampled = metric.Timestamp.AsTime().Format("2006-01-02T15:04:05Z")
	}
	var metricType string
	if metric.Data != nil {
		metricType = metric.Data.TypeUrl
	}

	output.PrintTable(
		[]string{"CONTAINER", "IMAGE", "STATUS", "SAMPLED", "METRIC TYPE"},
		[][]string{{id, info.Image, status, sampled, metricType}},
	)
	return nil
}
