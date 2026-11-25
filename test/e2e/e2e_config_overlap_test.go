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

package e2e

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
)

// RHWA-2063 (API-04), part 1: configuration overlap, assignment and
// single-StorageClass validation, end to end.
//
// Scope. These specs cover the *detection* half of the ticket only: the
// admission rejection, and the controller's re-check of assignments after
// admission. Part 2 (what a blocked configuration does to preparation,
// commitment and in-flight destructive actions) is a separate plan.
//
// Why e2e at all. The pure decision is already pinned by unit tests
// (api/v1alpha1/overlap_test.go, internal/controller/configconflicts_test.go),
// and a fake client never establishes admission behaviour. Only a live cluster
// shows that the webhook is wired up and rejecting, that two creates racing
// real admission end up in a safe state, and that relabelling a Node - which
// admission never sees - is caught by the controller.
//
// The contract these specs pin, for the implementer:
//
//  1. A conflicting create is rejected by the webhook. The error names the
//     conflicting configuration, and for a StorageClass conflict both class
//     names.
//  2. A conflict the webhook could not see (concurrent create, later relabel)
//     leaves the configuration with Ready=False and one of the conflict
//     reasons below, carrying the same detail in its message.
//  3. In every conflicting state, no node ends up with two BlockPerNode agents
//     on it. That invariant is the reason the ticket exists; it is asserted
//     throughout the wait, not only at the end.
//
// The reason strings below mirror the sentinel errors the unit tests already
// expect (ErrNodeSelectorOverlap, ErrStorageClassConflict). If the
// implementation names its condition reasons differently, this is the one
// place to change.
//
// Expected to fail today: BlockPerNode is API-only, the webhook has two TODOs
// where the overlap check belongs, and the controller defers to the webhook.

const (
	// overlapGroupLabel partitions real worker nodes into disjoint sets without
	// touching node roles.
	overlapGroupLabel = "sbr-e2e.medik8s.io/group"
	overlapGroupA     = "a"
	overlapGroupB     = "b"

	// Labels the operator puts on agent pods, used to find the agents a node
	// is carrying. Kept here so the invariant check does not depend on the
	// label strings being spelled out at each call site.
	agentAppLabelKey    = "app"
	agentAppLabelValue  = "sbr-agent"
	agentConfigLabelKey = "sbrconfig"

	// Condition reasons a blocked configuration is expected to carry.
	reasonNodeSelectorOverlap  = "NodeSelectorOverlap"
	reasonStorageClassConflict = "StorageClassConflict"

	// overlapOtherStorageClass is a second, deliberately different StorageClass
	// name. It does not have to exist: the single-class rule compares names,
	// and these configurations are never expected to provision anything.
	overlapOtherStorageClass = "sbr-e2e-other-sc"

	// overlapFallbackStorageClass is used when the cluster exposes no block
	// StorageClass. Same reasoning: admission and the conflict re-check do not
	// need the class to exist.
	overlapFallbackStorageClass = "sbr-e2e-blockpernode-sc"

	// How long the controller gets to publish a conflict it could only find
	// after admission, and how long a non-conflicting pair must stay clean.
	overlapConflictTimeout  = 2 * time.Minute
	overlapConflictInterval = 5 * time.Second
	overlapSettlePeriod     = 30 * time.Second

	// A single run of the concurrent-create spec usually serialises. Repeat it
	// so the interesting branch - both creates admitted - actually happens.
	// An in-body loop rather than MustPassRepeatedly, so each attempt can clean
	// up its own configurations before the next one starts.
	overlapRaceAttempts = 5
)

var _ = Describe("BlockPerNode configuration overlap (RHWA-2063)", Label("e2e", "blockpernode"), func() {
	var (
		nsPrimary        string
		nsSecondary      string
		nodeA            string
		nodeB            string
		primaryClass     string
		webhookInstalled bool
	)

	BeforeEach(func() {
		requireWorkerNodes(2)

		suffix := time.Now().UnixNano()
		nsPrimary = createOverlapNamespace(fmt.Sprintf("sbr-e2e-overlap-a-%d", suffix))
		nsSecondary = createOverlapNamespace(fmt.Sprintf("sbr-e2e-overlap-b-%d", suffix))

		// Two disjoint, single-node groups. Keeping them to one node each means
		// a conflict always names a node the spec knows about.
		nodeA = clusterInfo.WorkerNodes[0].Metadata.Name
		nodeB = clusterInfo.WorkerNodes[1].Metadata.Name
		labelNodeForOverlap(nodeA, overlapGroupA)
		labelNodeForOverlap(nodeB, overlapGroupB)

		primaryClass = overlapPrimaryStorageClass()
		webhookInstalled = configValidatingWebhookInstalled()
	})

	// ----------------------------------------------------------------- S1 ---

	It("accepts BlockPerNode configurations that select disjoint node sets", func() {
		// Negative control for everything below. Without it, an implementation
		// that rejects every second configuration passes the whole suite.
		zoneA := createOverlapConfig(blockPerNodeConfigFor(nsPrimary, "zone-a", primaryClass, groupSelector(overlapGroupA)))
		zoneB := createOverlapConfig(blockPerNodeConfigFor(nsSecondary, "zone-b", primaryClass, groupSelector(overlapGroupB)))

		expectNoConflictReported(zoneA)
		expectNoConflictReported(zoneB)
	})

	It("accepts a shared-storage configuration alongside a BlockPerNode configuration", func() {
		// AC 5. Only BlockPerNode gives a node a watchdog owner, so a shared
		// configuration is not a second owner and must not be treated as one.
		shared := sharedConfigFor(nsSecondary, "shared-storage", overlapSharedStorageClass(), groupSelector(overlapGroupB))
		perNode := blockPerNodeConfigFor(nsPrimary, "per-node", primaryClass, groupSelector(overlapGroupA))

		createdShared := createOverlapConfig(shared)
		createdPerNode := createOverlapConfig(perNode)

		expectNoConflictReported(createdShared)
		expectNoConflictReported(createdPerNode)
	})

	// ----------------------------------------------------------------- S2 ---

	Context("when a conflicting configuration is created while the first one is visible", func() {
		BeforeEach(func() {
			skipUnlessWebhookInstalled(webhookInstalled)
		})

		DescribeTable("the webhook rejects the create",
			func(existingSelector, incomingSelector map[string]string, sameNamespace bool) {
				existing := createOverlapConfig(
					blockPerNodeConfigFor(nsPrimary, "existing", primaryClass, existingSelector))

				incomingNamespace := nsSecondary
				if sameNamespace {
					incomingNamespace = nsPrimary
				}
				incoming := blockPerNodeConfigFor(incomingNamespace, "incoming", primaryClass, incomingSelector)

				By("creating the second configuration")
				err := k8sClient.Create(ctx, incoming)
				Expect(err).To(HaveOccurred(), "overlapping configuration was admitted")
				registerOverlapConfigCleanup(incoming)

				Expect(err.Error()).To(ContainSubstring(existing.Name),
					"rejection does not name the conflicting configuration")
				if !sameNamespace {
					Expect(err.Error()).To(ContainSubstring(nsPrimary),
						"cross-namespace rejection does not name the other namespace")
				}

				By("confirming the rejected configuration was not stored")
				stored := &medik8sv1alpha1.StorageBasedRemediationConfig{}
				getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), stored)
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue(),
					"rejected configuration exists in the API: %v", getErr)
			},

			Entry("same namespace, identical selectors",
				groupSelector(overlapGroupA), groupSelector(overlapGroupA), true),

			// The case the ticket calls out: the webhook and the controller each
			// defer the cross-namespace check to the other, so nobody runs it.
			Entry("across namespaces, identical selectors",
				groupSelector(overlapGroupA), groupSelector(overlapGroupA), false),

			// Effective selectors, not literal maps: an omitted selector means
			// every Linux worker, which includes the explicitly selected one.
			Entry("across namespaces, incoming configuration omits its selector",
				groupSelector(overlapGroupA), nil, false),

			Entry("across namespaces, existing configuration omits its selector",
				nil, groupSelector(overlapGroupA), false),
		)

		// ------------------------------------------------------------- S3 ---

		It("rejects a second BlockPerNode configuration using a different StorageClass", func() {
			// Disjoint nodes on purpose: overlap cannot be the reason, so this
			// can only be the one-class-per-release rule (6.5 defers the
			// extension).
			existing := createOverlapConfig(
				blockPerNodeConfigFor(nsPrimary, "zone-a", primaryClass, groupSelector(overlapGroupA)))

			incoming := blockPerNodeConfigFor(nsSecondary, "zone-b", overlapOtherStorageClass, groupSelector(overlapGroupB))
			err := k8sClient.Create(ctx, incoming)
			registerOverlapConfigCleanup(incoming)

			Expect(err).To(HaveOccurred(), "second StorageClass was admitted")
			for _, want := range []string{primaryClass, overlapOtherStorageClass, existing.Name} {
				Expect(err.Error()).To(ContainSubstring(want),
					"rejection does not mention %q", want)
			}
		})
	})

	// ----------------------------------------------------------------- S4 ---

	It("reconciles two concurrent creates to a blocked state with a visible reason", func() {
		for attempt := 1; attempt <= overlapRaceAttempts; attempt++ {
			By(fmt.Sprintf("attempt %d of %d", attempt, overlapRaceAttempts))
			runConcurrentCreateAttempt(nsPrimary, nsSecondary, primaryClass, nodeA)
		}
	})

	// ----------------------------------------------------------------- S5 ---

	It("blocks when a node is relabelled into a second configuration", func() {
		// Admission saw two disjoint configurations and was right to admit
		// them. The relabel happens afterwards, where only the controller can
		// see it.
		zoneA := createOverlapConfig(blockPerNodeConfigFor(nsPrimary, "zone-a", primaryClass, groupSelector(overlapGroupA)))
		zoneB := createOverlapConfig(blockPerNodeConfigFor(nsSecondary, "zone-b", primaryClass, groupSelector(overlapGroupB)))

		expectNoConflictReported(zoneA)

		By(fmt.Sprintf("moving node %s from group %s into group %s", nodeB, overlapGroupB, overlapGroupA))
		// Watch the invariant for the whole transition, not just afterwards: a
		// window with two armed agents is exactly the failure being tested.
		stopWatching := watchNodeContention(nodeB, zoneA.Name, zoneB.Name)
		labelNodeForOverlap(nodeB, overlapGroupA)

		By("waiting for the controller to report the conflict")
		expectConflictReported(zoneA, zoneB, reasonNodeSelectorOverlap, nodeB)
		stopWatching()

		By("relabelling the node back and expecting the conflict to clear")
		labelNodeForOverlap(nodeB, overlapGroupB)
		Eventually(func() error {
			for _, cfg := range []*medik8sv1alpha1.StorageBasedRemediationConfig{zoneA, zoneB} {
				reason, err := conflictReasonOf(cfg)
				if err != nil {
					return err
				}
				if reason != "" {
					return fmt.Errorf("%s/%s still blocked with reason %q", cfg.Namespace, cfg.Name, reason)
				}
			}
			return nil
		}, overlapConflictTimeout, overlapConflictInterval).Should(Succeed(),
			"the conflict is latched: it did not clear after the node left the second configuration")
	})
})

// runConcurrentCreateAttempt issues two conflicting creates with no ordering
// between them and asserts the cluster lands in a safe state, whichever way
// the race went. It cleans up whatever it created before returning.
func runConcurrentCreateAttempt(nsFirst, nsSecond, storageClass, contestedNode string) {
	first := blockPerNodeConfigFor(nsFirst, "race-first", storageClass, groupSelector(overlapGroupA))
	second := blockPerNodeConfigFor(nsSecond, "race-second", storageClass, groupSelector(overlapGroupA))

	defer func() {
		deleteOverlapConfig(first)
		deleteOverlapConfig(second)
	}()

	// Gomega assertions are not safe off the main goroutine, so the creates
	// only collect their errors.
	var (
		wg        sync.WaitGroup
		errFirst  error
		errSecond error
	)
	wg.Add(2)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		errFirst = k8sClient.Create(ctx, first)
	}()
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		errSecond = k8sClient.Create(ctx, second)
	}()
	wg.Wait()

	admitted := make([]*medik8sv1alpha1.StorageBasedRemediationConfig, 0, 2)
	if errFirst == nil {
		admitted = append(admitted, first)
	}
	if errSecond == nil {
		admitted = append(admitted, second)
	}
	Expect(admitted).NotTo(BeEmpty(),
		"both creates were rejected: first=%v second=%v", errFirst, errSecond)

	switch len(admitted) {
	case 1:
		// Admission serialised the two requests. Nothing to re-check, but the
		// survivor must not be blocked by the configuration that never existed.
		By("admission serialised the creates; asserting the survivor is not blocked")
		expectNoConflictReported(admitted[0])
	case 2:
		// The branch this spec exists for: neither create saw the other.
		By("both creates were admitted; asserting the controller blocks at least one")
		expectConflictReported(admitted[0], admitted[1], reasonNodeSelectorOverlap, contestedNode)
	}

	expectAtMostOneAgentOnNode(contestedNode, first.Name, second.Name)
}

// --------------------------------------------------------------------------
// Assertions
// --------------------------------------------------------------------------

// expectConflictReported waits for at least one of the two configurations to
// report the given conflict reason, and holds the no-two-agents invariant on
// the contested node while it waits.
func expectConflictReported(
	a, b *medik8sv1alpha1.StorageBasedRemediationConfig,
	wantReason, contestedNode string,
) {
	Eventually(func() error {
		for _, cfg := range []*medik8sv1alpha1.StorageBasedRemediationConfig{a, b} {
			condition, err := readyConditionOf(cfg)
			if err != nil {
				return err
			}
			if condition == nil || condition.Status != metav1.ConditionFalse {
				continue
			}
			if condition.Reason != wantReason {
				continue
			}
			// "with a visible reason": the message has to say which other
			// configuration, and which node, so an admin can act on it.
			other := b
			if cfg == b {
				other = a
			}
			for _, want := range []string{other.Name, contestedNode} {
				if !strings.Contains(condition.Message, want) {
					return fmt.Errorf("%s/%s blocked with reason %q but the message %q does not mention %q",
						cfg.Namespace, cfg.Name, condition.Reason, condition.Message, want)
				}
			}
			return nil
		}
		return fmt.Errorf("neither %s/%s nor %s/%s reports %s (%s)",
			a.Namespace, a.Name, b.Namespace, b.Name, wantReason, describeConflictState(a, b))
	}, overlapConflictTimeout, overlapConflictInterval).Should(Succeed())
}

// expectNoConflictReported holds that a configuration stays free of any
// conflict reason. It deliberately does not require Ready=True: a BlockPerNode
// configuration may legitimately fail to become ready for storage reasons that
// have nothing to do with this ticket.
func expectNoConflictReported(cfg *medik8sv1alpha1.StorageBasedRemediationConfig) {
	Consistently(func() error {
		reason, err := conflictReasonOf(cfg)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("%s/%s was blocked with reason %q", cfg.Namespace, cfg.Name, reason)
		}
		return nil
	}, overlapSettlePeriod, overlapConflictInterval).Should(Succeed())
}

// expectAtMostOneAgentOnNode asserts the invariant the ticket is about: a node
// never has agents from two BlockPerNode configurations at the same time.
func expectAtMostOneAgentOnNode(nodeName string, configNames ...string) {
	Expect(agentConfigsOnNode(nodeName, configNames)).To(BeEmpty(),
		"node %s carries agents from more than one BlockPerNode configuration", nodeName)
}

// watchNodeContention polls the invariant in the background and returns a stop
// function that fails the spec if the invariant was ever broken. Used across a
// transition, where the end state can look safe even though a window was not.
func watchNodeContention(nodeName string, configNames ...string) func() {
	stop := make(chan struct{})
	done := make(chan []string, 1)

	go func() {
		defer GinkgoRecover()
		var seen []string
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- seen
				return
			case <-ticker.C:
				if owners := agentConfigsOnNode(nodeName, configNames); len(owners) > 1 {
					seen = owners
				}
			}
		}
	}()

	return func() {
		close(stop)
		owners := <-done
		Expect(owners).To(BeEmpty(),
			"node %s had agents from configurations %v at the same time during the transition",
			nodeName, owners)
	}
}

// agentConfigsOnNode returns the names of the given configurations that
// currently have a non-terminated agent pod on the node, but only when there
// is more than one - a single owner is the expected state.
func agentConfigsOnNode(nodeName string, configNames []string) []string {
	wanted := make(map[string]bool, len(configNames))
	for _, name := range configNames {
		wanted[name] = true
	}

	pods := &corev1.PodList{}
	if err := k8sClient.List(ctx, pods, client.MatchingLabels{agentAppLabelKey: agentAppLabelValue}); err != nil {
		// Treat an unreadable cluster as "cannot prove the invariant holds"
		// rather than as a violation; the surrounding Eventually will retry.
		GinkgoWriter.Printf("failed to list agent pods: %v\n", err)
		return nil
	}

	owners := map[string]bool{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != nodeName {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if owner := pod.Labels[agentConfigLabelKey]; wanted[owner] {
			owners[owner] = true
		}
	}
	if len(owners) < 2 {
		return nil
	}

	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	return names
}

// --------------------------------------------------------------------------
// Reading configuration state
// --------------------------------------------------------------------------

func readyConditionOf(cfg *medik8sv1alpha1.StorageBasedRemediationConfig) (*metav1.Condition, error) {
	live := &medik8sv1alpha1.StorageBasedRemediationConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cfg), live); err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}
	return live.GetCondition(medik8sv1alpha1.SBRConfigConditionReady), nil
}

// conflictReasonOf returns the conflict reason a configuration is blocked
// with, or "" when it is not blocked for a conflict.
func conflictReasonOf(cfg *medik8sv1alpha1.StorageBasedRemediationConfig) (string, error) {
	condition, err := readyConditionOf(cfg)
	if err != nil {
		return "", err
	}
	if condition == nil || condition.Status != metav1.ConditionFalse {
		return "", nil
	}
	switch condition.Reason {
	case reasonNodeSelectorOverlap, reasonStorageClassConflict:
		return condition.Reason, nil
	default:
		// Not ready for some unrelated reason (storage, image, ...). Not this
		// ticket's business.
		return "", nil
	}
}

// describeConflictState renders both configurations' Ready conditions for a
// failure message, so a timeout says what the controller actually published.
func describeConflictState(configs ...*medik8sv1alpha1.StorageBasedRemediationConfig) string {
	parts := make([]string, 0, len(configs))
	for _, cfg := range configs {
		condition, err := readyConditionOf(cfg)
		switch {
		case err != nil:
			parts = append(parts, fmt.Sprintf("%s/%s: %v", cfg.Namespace, cfg.Name, err))
		case condition == nil:
			parts = append(parts, fmt.Sprintf("%s/%s: no Ready condition", cfg.Namespace, cfg.Name))
		default:
			parts = append(parts, fmt.Sprintf("%s/%s: Ready=%s reason=%q message=%q",
				cfg.Namespace, cfg.Name, condition.Status, condition.Reason, condition.Message))
		}
	}
	return strings.Join(parts, "; ")
}

// --------------------------------------------------------------------------
// Fixtures
// --------------------------------------------------------------------------

func groupSelector(group string) map[string]string {
	return map[string]string{overlapGroupLabel: group}
}

func blockPerNodeConfigFor(
	namespace, name, storageClass string,
	selector map[string]string,
) *medik8sv1alpha1.StorageBasedRemediationConfig {
	mode := medik8sv1alpha1.SharedStorageVolumeModeBlockPerNode
	return &medik8sv1alpha1.StorageBasedRemediationConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
			SharedStorageVolumeMode: &mode,
			SharedStorageClass:      storageClass,
			NodeSelector:            selector,
			WatchdogPath:            medik8sv1alpha1.DefaultWatchdogPath,
		},
	}
}

func sharedConfigFor(
	namespace, name, storageClass string,
	selector map[string]string,
) *medik8sv1alpha1.StorageBasedRemediationConfig {
	mode := medik8sv1alpha1.SharedStorageVolumeModeFilesystem
	return &medik8sv1alpha1.StorageBasedRemediationConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: medik8sv1alpha1.StorageBasedRemediationConfigSpec{
			SharedStorageVolumeMode: &mode,
			SharedStorageClass:      storageClass,
			NodeSelector:            selector,
			WatchdogPath:            medik8sv1alpha1.DefaultWatchdogPath,
		},
	}
}

// overlapPrimaryStorageClass picks the StorageClass the BlockPerNode
// configurations name. These specs never provision, so a real class is a
// convenience, not a requirement.
func overlapPrimaryStorageClass() string {
	sc, err := findBlockStorageClass()
	Expect(err).NotTo(HaveOccurred(), "StorageClass discovery failed")
	if sc != nil {
		return sc.Name
	}
	GinkgoWriter.Printf("no block StorageClass found, using placeholder %q\n", overlapFallbackStorageClass)
	return overlapFallbackStorageClass
}

func overlapSharedStorageClass() string {
	sc, err := findRWXFilesystemStorageClass()
	Expect(err).NotTo(HaveOccurred(), "StorageClass discovery failed")
	if sc != nil {
		return sc.Name
	}
	return overlapFallbackStorageClass
}

// createOverlapConfig creates a configuration that is expected to be admitted
// and registers its cleanup.
func createOverlapConfig(
	cfg *medik8sv1alpha1.StorageBasedRemediationConfig,
) *medik8sv1alpha1.StorageBasedRemediationConfig {
	Expect(k8sClient.Create(ctx, cfg)).To(Succeed(),
		"configuration %s/%s was rejected but should have been admitted", cfg.Namespace, cfg.Name)
	registerOverlapConfigCleanup(cfg)
	return cfg
}

func registerOverlapConfigCleanup(cfg *medik8sv1alpha1.StorageBasedRemediationConfig) {
	DeferCleanup(func() {
		deleteOverlapConfig(cfg)
	})
}

// deleteOverlapConfig removes a configuration and waits for it to go. A
// configuration carries a finalizer; if the operator never clears it - likely
// while BlockPerNode is unimplemented - the finalizer is stripped so the spec
// does not wedge the namespace for the rest of the suite.
func deleteOverlapConfig(cfg *medik8sv1alpha1.StorageBasedRemediationConfig) {
	live := &medik8sv1alpha1.StorageBasedRemediationConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cfg), live); err != nil {
		if !apierrors.IsNotFound(err) {
			GinkgoWriter.Printf("failed to get configuration %s/%s for deletion: %v\n", cfg.Namespace, cfg.Name, err)
		}
		return
	}
	if err := k8sClient.Delete(ctx, live); err != nil && !apierrors.IsNotFound(err) {
		GinkgoWriter.Printf("failed to delete configuration %s/%s: %v\n", cfg.Namespace, cfg.Name, err)
	}

	gone := func() bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cfg), &medik8sv1alpha1.StorageBasedRemediationConfig{})
		return apierrors.IsNotFound(err)
	}

	// A plain Eventually would fail the spec here and skip the fallback, so
	// the wait is a loop: deletion that never finishes is a cleanup problem,
	// not a result.
	deadline := time.Now().Add(2 * time.Minute)
	for !gone() && time.Now().Before(deadline) {
		time.Sleep(overlapConflictInterval)
	}
	if gone() {
		return
	}

	GinkgoWriter.Printf("configuration %s/%s still present after deletion; stripping its finalizers\n",
		cfg.Namespace, cfg.Name)
	stuck := &medik8sv1alpha1.StorageBasedRemediationConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cfg), stuck); err == nil {
		patch := client.MergeFrom(stuck.DeepCopy())
		stuck.Finalizers = nil
		if err := k8sClient.Patch(ctx, stuck, patch); err != nil {
			GinkgoWriter.Printf("failed to strip finalizers from %s/%s: %v\n", cfg.Namespace, cfg.Name, err)
		}
	}
}

func createOverlapNamespace(name string) string {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace %s", name)
	}
	DeferCleanup(func() {
		if err := k8sClient.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
			GinkgoWriter.Printf("failed to delete namespace %s: %v\n", name, err)
		}
	})
	return name
}

// labelNodeForOverlap sets the group label on a real cluster node and restores
// the node's original labelling at the end of the spec.
func labelNodeForOverlap(nodeName, group string) {
	// Only the first call for a node registers the restore; a later relabel in
	// the same spec must not queue a second one.
	_, alreadyLabelled := nodeOverlapGroup(nodeName)

	Eventually(func() error {
		node := &corev1.Node{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return err
		}
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels[overlapGroupLabel] = group
		return k8sClient.Update(ctx, node)
	}, time.Minute, time.Second).Should(Succeed(), "failed to label node %s", nodeName)

	if alreadyLabelled {
		return
	}
	DeferCleanup(func() {
		Eventually(func() error {
			node := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			if _, present := node.Labels[overlapGroupLabel]; !present {
				return nil
			}
			delete(node.Labels, overlapGroupLabel)
			return k8sClient.Update(ctx, node)
		}, time.Minute, time.Second).Should(Succeed(),
			"failed to remove the group label from node %s; later specs will see a stale assignment", nodeName)
	})
}

func nodeOverlapGroup(nodeName string) (string, bool) {
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return "", false
	}
	value, present := node.Labels[overlapGroupLabel]
	return value, present
}

func requireWorkerNodes(count int) {
	if len(clusterInfo.WorkerNodes) < count {
		Skip(fmt.Sprintf("these specs need at least %d worker nodes to build disjoint node sets, cluster has %d",
			count, len(clusterInfo.WorkerNodes)))
	}
}

// configValidatingWebhookInstalled reports whether a validating webhook covers
// StorageBasedRemediationConfig. Admission behaviour cannot be asserted
// without it.
func configValidatingWebhookInstalled() bool {
	list := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	if err := k8sClient.List(ctx, list); err != nil {
		GinkgoWriter.Printf("failed to list validating webhook configurations: %v\n", err)
		return false
	}
	for i := range list.Items {
		for _, webhook := range list.Items[i].Webhooks {
			for _, rule := range webhook.Rules {
				for _, resource := range rule.Resources {
					if strings.HasPrefix(resource, "storagebasedremediationconfigs") {
						return true
					}
				}
			}
		}
	}
	return false
}

func skipUnlessWebhookInstalled(installed bool) {
	if !installed {
		Skip("no validating webhook for storagebasedremediationconfigs is installed; " +
			"admission rejection cannot be asserted on this cluster")
	}
}
