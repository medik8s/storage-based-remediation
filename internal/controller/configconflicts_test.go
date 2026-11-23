/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
)

// RHWA-2063 (API-04), part 1: the controller call site.
//
// Admission cannot see two configurations created at the same instant, and it
// never sees a Node relabelled afterwards. The controller therefore has to
// re-run the same check on every reconcile. These tests cover the detection
// only; turning a detected conflict into a blocked state with a visible reason
// is part 2 of the ticket.

const (
	conflictZoneLabel = "topology.kubernetes.io/zone"
	conflictOSLabel   = "kubernetes.io/os"
	conflictRoleLabel = "node-role.kubernetes.io/worker"
	conflictPoolLabel = "storage.example.com/pool"
)

// TestBuildNodeSelectorUsesEffectiveSelector pins the DaemonSet's node
// selector to the same function the conflict check uses. If these two ever
// diverge, the operator schedules agents onto nodes the overlap check did not
// consider, which is exactly the two-watchdog-owners failure.
func TestBuildNodeSelectorUsesEffectiveSelector(t *testing.T) {
	reconciler := &StorageBasedRemediationConfigReconciler{}

	for _, selector := range []map[string]string{
		nil,
		{},
		{conflictZoneLabel: "a"},
		{conflictOSLabel: "windows"},
		{conflictRoleLabel: "", conflictZoneLabel: "b"},
	} {
		cfg := &medik8sv1alpha1.StorageBasedRemediationConfig{
			Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{NodeSelector: selector},
		}
		got := reconciler.buildNodeSelector(cfg)
		want := cfg.Spec.EffectiveNodeSelector()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("buildNodeSelector(%v) = %v, want %v", selector, got, want)
		}
	}
}

var _ = Describe("BlockPerNode configuration conflict re-check", func() {
	ctx := context.Background()

	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	var (
		namespace            string
		otherNamespace       string
		nodeNames            []string
		controllerReconciler *StorageBasedRemediationConfigReconciler
	)

	// newNode creates a labelled Node and registers it for cleanup.
	newNode := func(name string, labels map[string]string) {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		nodeNames = append(nodeNames, name)
	}

	// relabel replaces one label on an existing Node.
	relabel := func(name, key, value string) {
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels[key] = value
		Expect(k8sClient.Update(ctx, node)).To(Succeed())
	}

	// newConfig creates a BlockPerNode configuration directly, bypassing
	// admission. That is the point: it is how two racing creates and a
	// webhook-free envtest both look to the controller.
	newConfig := func(ns, name string, selector map[string]string) *medik8sv1alpha1.StorageBasedRemediationConfig {
		mode := medik8sv1alpha1.SharedStorageVolumeModeBlockPerNode
		cfg := &medik8sv1alpha1.StorageBasedRemediationConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
				SharedStorageVolumeMode: &mode,
				SharedStorageClass:      validSharedStorageClass,
				NodeSelector:            selector,
			},
		}
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		return cfg
	}

	BeforeEach(func() {
		suffix := time.Now().UnixNano()
		namespace = fmt.Sprintf("test-conflict-%d", suffix)
		otherNamespace = fmt.Sprintf("test-conflict-other-%d", suffix)
		nodeNames = nil

		for _, ns := range []string{namespace, otherNamespace} {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: ns},
			})).To(Succeed())
		}

		newNode(fmt.Sprintf("conflict-node-a-%d", suffix), map[string]string{
			conflictRoleLabel: "", conflictOSLabel: "linux", conflictZoneLabel: "a",
		})
		// The second node also carries an independent storage label, so a
		// relabel can move it into another configuration's selector without
		// moving it out of its own.
		newNode(fmt.Sprintf("conflict-node-b-%d", suffix), map[string]string{
			conflictRoleLabel: "", conflictOSLabel: "linux", conflictZoneLabel: "b",
			conflictPoolLabel: "fast",
		})

		controllerReconciler = &StorageBasedRemediationConfigReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	AfterEach(func() {
		// envtest has no node controller, so Nodes outlive the spec unless
		// they are removed here.
		for _, name := range nodeNames {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
		}
		for _, ns := range []string{namespace, otherNamespace} {
			configs := &medik8sv1alpha1.StorageBasedRemediationConfigList{}
			Expect(k8sClient.List(ctx, configs, client.InNamespace(ns))).To(Succeed())
			for i := range configs.Items {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &configs.Items[i]))).To(Succeed())
			}
		}
	})

	It("accepts configurations selecting disjoint node sets", func() {
		zoneA := newConfig(namespace, "zone-a", map[string]string{conflictZoneLabel: "a"})
		newConfig(otherNamespace, "zone-b", map[string]string{conflictZoneLabel: "b"})

		Expect(controllerReconciler.checkConfigConflicts(ctx, zoneA)).To(Succeed())
	})

	It("reports a conflict for two configurations that both won admission", func() {
		// Neither create saw the other, so both are stored. The controller has
		// to reach the same conclusion from whichever one it reconciles, with
		// no dependence on creation order.
		first := newConfig(namespace, "all-workers", nil)
		second := newConfig(otherNamespace, "zone-a", map[string]string{conflictZoneLabel: "a"})

		Expect(controllerReconciler.checkConfigConflicts(ctx, first)).
			To(MatchError(medik8sv1alpha1.ErrNodeSelectorOverlap))
		Expect(controllerReconciler.checkConfigConflicts(ctx, second)).
			To(MatchError(medik8sv1alpha1.ErrNodeSelectorOverlap))
	})

	It("reports a conflict only after a node is relabelled into a second configuration", func() {
		// Admission saw two disjoint configurations and was right to accept
		// them. The relabel happens afterwards, where only the controller can
		// see it.
		zoneA := newConfig(namespace, "zone-a", map[string]string{conflictZoneLabel: "a"})
		newConfig(otherNamespace, "pool-fast", map[string]string{conflictPoolLabel: "fast"})

		Expect(controllerReconciler.checkConfigConflicts(ctx, zoneA)).To(Succeed())

		By("moving the pool-fast node into zone a")
		relabel(nodeNames[1], conflictZoneLabel, "a")

		Eventually(func() error {
			return controllerReconciler.checkConfigConflicts(ctx, zoneA)
		}, timeout, interval).Should(MatchError(medik8sv1alpha1.ErrNodeSelectorOverlap))
	})

	It("clears the conflict when the node is relabelled back", func() {
		zoneA := newConfig(namespace, "zone-a", map[string]string{conflictZoneLabel: "a"})
		newConfig(otherNamespace, "pool-fast", map[string]string{conflictPoolLabel: "fast"})

		relabel(nodeNames[1], conflictZoneLabel, "a")
		Eventually(func() error {
			return controllerReconciler.checkConfigConflicts(ctx, zoneA)
		}, timeout, interval).Should(HaveOccurred())

		relabel(nodeNames[1], conflictZoneLabel, "b")
		Eventually(func() error {
			return controllerReconciler.checkConfigConflicts(ctx, zoneA)
		}, timeout, interval).Should(Succeed())
	})

	It("clears the conflict when the other configuration is deleted", func() {
		subject := newConfig(namespace, "all-workers", nil)
		other := newConfig(otherNamespace, "also-all-workers", nil)

		Expect(controllerReconciler.checkConfigConflicts(ctx, subject)).To(HaveOccurred())

		Expect(k8sClient.Delete(ctx, other)).To(Succeed())
		Eventually(func() error {
			return controllerReconciler.checkConfigConflicts(ctx, subject)
		}, timeout, interval).Should(Succeed())
	})

	It("reports a conflict for a second StorageClass on disjoint nodes", func() {
		zoneA := newConfig(namespace, "zone-a", map[string]string{conflictZoneLabel: "a"})

		zoneB := newConfig(otherNamespace, "zone-b", map[string]string{conflictZoneLabel: "b"})
		zoneB.Spec.SharedStorageClass = invalidSharedStorageClass
		Expect(k8sClient.Update(ctx, zoneB)).To(Succeed())

		Eventually(func() error {
			return controllerReconciler.checkConfigConflicts(ctx, zoneA)
		}, timeout, interval).Should(MatchError(medik8sv1alpha1.ErrStorageClassConflict))
	})

	It("does not conflict with a shared-storage configuration on the same nodes", func() {
		// AC 5: shared storage and BlockPerNode coexist. Only BlockPerNode
		// gives a node a watchdog owner, so only BlockPerNode pairs matter.
		filesystem := medik8sv1alpha1.SharedStorageVolumeModeFilesystem
		shared := &medik8sv1alpha1.StorageBasedRemediationConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: otherNamespace, Name: "shared"},
			Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
				SharedStorageVolumeMode: &filesystem,
				SharedStorageClass:      validSharedStorageClass,
			},
		}
		Expect(k8sClient.Create(ctx, shared)).To(Succeed())

		perNode := newConfig(namespace, "per-node", nil)
		Expect(controllerReconciler.checkConfigConflicts(ctx, perNode)).To(Succeed())
	})

	It("does not conflict with itself", func() {
		subject := newConfig(namespace, "only-one", nil)

		Consistently(func() error {
			return controllerReconciler.checkConfigConflicts(ctx, subject)
		}, time.Second, interval).Should(Succeed())
	})

	It("treats an unreadable cluster as a conflict rather than a pass", func() {
		subject := newConfig(namespace, "all-workers", nil)

		failing := &StorageBasedRemediationConfigReconciler{
			Client: &failingListClient{Client: k8sClient, err: errors.New("list unavailable")},
			Scheme: k8sClient.Scheme(),
		}
		err := failing.checkConfigConflicts(ctx, subject)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, medik8sv1alpha1.ErrConflictCheckUnavailable)).To(BeTrue(),
			"expected ErrConflictCheckUnavailable, got %v", err)
	})
})

// failingListClient fails every List and delegates everything else.
type failingListClient struct {
	client.Client
	err error
}

func (c *failingListClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}
