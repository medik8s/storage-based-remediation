package blockpernode

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestConditionalStoragePatchWithAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is required for real API checks")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	}()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "patch-target", Annotations: map[string]string{"example.test/unrelated": "keep"}}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now(), Reason: "KubeletReady", Message: "ready"}}
	if err := c.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	stale := node.DeepCopy()
	if err := PatchCondition(ctx, c, node, corev1.ConditionTrue, "HeartbeatTimeout", "target failed", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := PatchCondition(ctx, c, stale, corev1.ConditionFalse, "Recovered", "stale recovery", time.Now()); !apierrors.IsConflict(err) {
		t.Fatalf("stale version error %v", err)
	}
	current := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations["example.test/unrelated"] != "keep" || len(current.Status.Conditions) != 2 {
		t.Fatalf("unrelated fields changed: %+v", current)
	}
	version := current.ResourceVersion
	transition := Condition(current).LastTransitionTime
	if err := PatchCondition(ctx, c, current, corev1.ConditionTrue, "ignored", "same status", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		t.Fatal(err)
	}
	if current.ResourceVersion != version || !Condition(current).LastTransitionTime.Equal(&transition) {
		t.Fatal("same-status request changed the Node")
	}
	if err := PatchCondition(ctx, c, current, corev1.ConditionFalse, "Recovered", "fresh recovery", time.Now()); err != nil {
		t.Fatal(err)
	}
	otherWriter, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, reasons := range [][2]string{{"HeartbeatTimeout", "StorageProbeRecovered"}, {"StorageProbeFailed", "Recovered"}} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
			t.Fatal(err)
		}
		for index := range current.Status.Conditions {
			if current.Status.Conditions[index].Type == corev1.NodeConditionType(StorageCondition) {
				current.Status.Conditions[index].Status = corev1.ConditionUnknown
			}
		}
		if err := c.Status().Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		first, second := current.DeepCopy(), current.DeepCopy()
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- PatchCondition(ctx, c, first, corev1.ConditionTrue, reasons[0], "failure evidence", time.Now())
		}()
		go func() {
			<-start
			results <- PatchCondition(ctx, otherWriter, second, corev1.ConditionFalse, reasons[1], "recovery evidence", time.Now())
		}()
		close(start)
		accepted, conflicts := 0, 0
		for range 2 {
			switch result := <-results; {
			case result == nil:
				accepted++
			case apierrors.IsConflict(result):
				conflicts++
			default:
				t.Fatalf("competing writer failed: %v", result)
			}
		}
		if accepted != 1 || conflicts != 1 {
			t.Fatalf("competing writes accepted=%d conflicts=%d", accepted, conflicts)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
			t.Fatal(err)
		}
		if current.Annotations["example.test/unrelated"] != "keep" || len(current.Status.Conditions) != 2 {
			t.Fatal("competing writers changed unrelated fields")
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == corev1.NodeReady && (condition.Status != corev1.ConditionTrue || condition.Reason != "KubeletReady" || condition.Message != "ready") {
				t.Fatal("competing writers changed the Ready condition")
			}
		}
	}
	if err := PatchCondition(ctx, c, current, corev1.ConditionTrue, "StorageProbeFailed", "local failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		t.Fatal(err)
	}
	version = current.ResourceVersion
	transition = Condition(current).LastTransitionTime
	if err := PatchCondition(ctx, otherWriter, current, corev1.ConditionTrue, "StorageProbeFailed", "later local failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		t.Fatal(err)
	}
	if current.ResourceVersion != version || !Condition(current).LastTransitionTime.Equal(&transition) {
		t.Fatal("same-status local failure created a visible transition")
	}
	// A current version cannot order recovery against an unpublished same-status failure.
	if err := PatchCondition(ctx, otherWriter, current, corev1.ConditionFalse, "Recovered", "watcher progress", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		t.Fatal(err)
	}
	if Condition(current).Status != corev1.ConditionFalse || Condition(current).Reason != "Recovered" {
		t.Fatal("matching current recovery was rejected")
	}
}
