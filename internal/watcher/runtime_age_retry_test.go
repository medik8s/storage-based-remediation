package watcher

import (
	"context"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

type retryAgeClient struct {
	client.Client
	t               *testing.T
	patchVersions   []string
	liveVersions    []string
	changedVersions []string
	patchDeadlines  []time.Time
	getDeadlines    []time.Time
	conflicts       int
	successes       int
}

func (c *retryAgeClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	c.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		c.t.Fatal("transition live GET has no deadline")
	}
	c.getDeadlines = append(c.getDeadlines, deadline)
	if err := c.Client.Get(ctx, key, object, options...); err != nil {
		return err
	}
	c.liveVersions = append(c.liveVersions, object.GetResourceVersion())
	return nil
}

func (c *retryAgeClient) Status() client.SubResourceWriter {
	return &retryAgeStatus{SubResourceWriter: c.Client.Status(), owner: c}
}

type retryAgeStatus struct {
	client.SubResourceWriter
	owner *retryAgeClient
}

func (c *retryAgeStatus) Patch(ctx context.Context, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
	c.owner.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		c.owner.t.Fatal("transition status patch has no deadline")
	}
	c.owner.patchDeadlines = append(c.owner.patchDeadlines, deadline)
	c.owner.patchVersions = append(c.owner.patchVersions, object.GetResourceVersion())
	attempt := len(c.owner.patchVersions)
	if attempt <= 2 {
		unrelated := &corev1.Node{}
		if err := c.owner.Client.Get(ctx, client.ObjectKeyFromObject(object), unrelated); err != nil {
			c.owner.t.Fatalf("read Node before unrelated write %d: %v", attempt, err)
		}
		unrelated.Annotations["test.example/unrelated-write"] = strconv.Itoa(attempt)
		if err := c.owner.Client.Update(ctx, unrelated); err != nil {
			c.owner.t.Fatalf("write unrelated annotation %d: %v", attempt, err)
		}
		c.owner.changedVersions = append(c.owner.changedVersions, unrelated.ResourceVersion)
	}
	err := c.SubResourceWriter.Patch(ctx, object, patch, options...)
	if attempt <= 2 {
		if !apierrors.IsConflict(err) {
			c.owner.t.Fatalf("patch %d error=%v, expected resource-version conflict", attempt, err)
		}
		c.owner.conflicts++
	}
	if err == nil {
		c.owner.successes++
	}
	return err
}

func TestTransitionConflictsKeepOriginalSampleDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, base := runtimeFixture("a")
		target := r.Assignment.Targets[0]
		target.Timing.ProbeInterval.Duration = 100 * time.Millisecond
		target.Timing.IOTimeout.Duration = 100 * time.Millisecond
		node, epoch, available := r.Nodes.Snapshot(target.Name)
		if !available {
			t.Fatal("initial Node state is unavailable")
		}
		counted := &retryAgeClient{Client: base, t: t}
		r.Client = counted
		sampled := time.Now()
		r.transition(context.Background(), target, node, epoch, corev1.ConditionFalse, "Recovered", sampled)
		if counted.conflicts != 2 || len(counted.patchVersions) != 2 || len(counted.liveVersions) != 2 || counted.successes != 0 {
			t.Fatalf("transition operations: conflicts=%d patches=%d live GETs=%d successful patches=%d", counted.conflicts, len(counted.patchVersions), len(counted.liveVersions), counted.successes)
		}
		if elapsed := time.Since(sampled); elapsed != target.Timing.Gap() {
			t.Fatalf("sample lifetime=%s expected original G=%s", elapsed, target.Timing.Gap())
		}
		if counted.patchVersions[0] != node.ResourceVersion || counted.patchVersions[1] != counted.liveVersions[0] {
			t.Fatalf("patch candidates=%v initial=%s live versions=%v", counted.patchVersions, node.ResourceVersion, counted.liveVersions)
		}
		for index, version := range counted.changedVersions {
			if version == counted.patchVersions[index] || counted.liveVersions[index] != version {
				t.Fatalf("unrelated write %d: patch version=%s changed version=%s live version=%s", index+1, counted.patchVersions[index], version, counted.liveVersions[index])
			}
		}
		for index, deadline := range counted.patchDeadlines {
			expected := sampled.Add(time.Duration(index+1) * target.Timing.ProbeInterval.Duration)
			if !deadline.Equal(expected) {
				t.Fatalf("patch %d deadline=%s expected=%s", index+1, deadline, expected)
			}
			if !counted.getDeadlines[index].Equal(expected) {
				t.Fatalf("live GET %d deadline=%s expected=%s", index+1, counted.getDeadlines[index], expected)
			}
		}
		current := &corev1.Node{}
		if err := base.Get(context.Background(), client.ObjectKeyFromObject(node), current); err != nil {
			t.Fatalf("read final Node: %v", err)
		}
		if !blockpernode.SameCondition(blockpernode.Condition(node), blockpernode.Condition(current)) {
			t.Fatalf("expired retries changed the storage condition: %+v", blockpernode.Condition(current))
		}
		if current.Annotations["test.example/unrelated-write"] != "2" || current.ResourceVersion != counted.liveVersions[1] {
			t.Fatalf("final unrelated data: annotations=%v resourceVersion=%s live versions=%v", current.Annotations, current.ResourceVersion, counted.liveVersions)
		}
		_, currentEpoch, available := r.Nodes.Snapshot(target.Name)
		if !available || currentEpoch != epoch {
			t.Fatalf("unrelated conflicts changed authority: available=%t epoch=%d expected=%d", available, currentEpoch, epoch)
		}
		time.Sleep(target.Timing.Gap())
		r.transition(context.Background(), target, current, epoch, corev1.ConditionFalse, "Recovered", sampled)
		if len(counted.patchVersions) != 2 || len(counted.liveVersions) != 2 || counted.successes != 0 {
			t.Fatalf("expired sample was reused: patches=%d live GETs=%d successes=%d", len(counted.patchVersions), len(counted.liveVersions), counted.successes)
		}
	})
}
