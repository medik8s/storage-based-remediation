# Package Name Change

## Overview

Starting from v5.8.0, the package name for the Storage Based Remediation community operator has been changed from `storage-based-remediation` to `medik8s-storage-based-remediation`.

## Reason for Change

When multiple catalog sources each publish their own variant of the same
operator under an identical OLM package name, OLM and tooling such as
`packagemanifests` can't reliably tell the variants apart, even if they
share the same name, CRDs, etc. Prefixing the package name with
`medik8s-` makes it unique, so it can always be unambiguously resolved to
our operator regardless of which catalog source it's queried from.

## Breaking Change

This is a **breaking change** for users of `storage-based-remediation` via OLM. Because OLM treats different package names as entirely separate operators, there is no automatic path from `storage-based-remediation` to `medik8s-storage-based-remediation` — the old operator must be uninstalled and the new one installed fresh.

## Migration Instructions

Moving from the old package name to the new one requires manually uninstalling the old OLM Subscription/CSV and installing a fresh Subscription for the new package, as described below.

> [!NOTE]
> We recommend **not** deleting your `StorageBasedRemediationConfig` or `StorageBasedRemediation` custom resources as part of this migration.
> The newly installed operator can pick them up automatically in Step 4, so removing these CRs isn't required.

### Step 1: Delete the Old Subscription
Delete the Subscription associated with the old package name. This will **not** delete your `StorageBasedRemediationConfig` or `StorageBasedRemediation` custom resources — do not delete them yourself.

```bash
# Find the subscription name
kubectl get subscription -n openshift-operators | grep storage-based-remediation

# Delete the subscription
kubectl delete subscription <subscription-name> -n openshift-operators
```

### Step 2: Delete the Old ClusterServiceVersion (CSV)

> [!WARNING]
> Again, only delete the CSV itself. Do **not** delete your `StorageBasedRemediationConfig` or `StorageBasedRemediation` CRs.

```bash
# Find the CSV name
kubectl get csv -n openshift-operators | grep storage-based-remediation

# Delete the CSV
kubectl delete csv <csv-name> -n openshift-operators
```

### Step 3: Install the New Package

Log in as a user with `cluster-admin` privileges before proceeding.

#### Method A: OpenShift Console (Recommended)
1. Navigate to **Operators** -> **OperatorHub**.
2. Search for **"Medik8s Storage-Based Remediation"**.
3. Click **Install**, keeping the default installation mode/namespace so the Operator lands in the same namespace as the previous installation.
4. To confirm the install succeeded, go to **Operators** -> **Installed Operators** and check that its status is **Succeeded**. If not, check the **Status** column for errors and inspect the controller-manager/agent pod logs in that namespace.

#### Method B: CLI (Subscription YAML)
Create a new Subscription using the new package name.

**Example Subscription (`sbr-new-subscription.yaml`):**
```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: medik8s-storage-based-remediation
  namespace: openshift-operators
spec:
  channel: stable
  name: medik8s-storage-based-remediation
  source: community-operators 
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
```

Apply the new subscription:
```bash
kubectl apply -f sbr-new-subscription.yaml
```

### Step 4: Verify the New Installation
Once the new Subscription is created, OLM will install the new CSV. The operator will start and automatically pick up your existing `StorageBasedRemediationConfig` and `StorageBasedRemediation` resources.

```bash
# Check the new CSV status
kubectl get csv -n openshift-operators | grep medik8s-storage-based-remediation

# Verify the operator pods are running
kubectl get pods -n openshift-operators | grep medik8s-storage-based-remediation

# Confirm your existing CRs are still present and picked up by the new operator
kubectl get storagebasedremediationconfig -A
kubectl get storagebasedremediation -A
```
