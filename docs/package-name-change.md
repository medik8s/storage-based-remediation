# Package Name Change

## Overview

Starting from v5.8.0, the package name for the Storage Based Remediation community operator has been changed from `storage-based-remediation` to `medik8s-storage-based-remediation`.

## Reason for Change

This change was made to resolve non-deterministic results when querying `packagemanifests`. 
The original package name `storage-based-remediation` existed in both the `redhat-operators` and `community-operators` catalog sources. 
Prefixing the package name with `medik8s-` ensures uniqueness across catalog sources.

## Breaking Change

This is a **breaking change** for users upgrading via OLM. Because OLM treats different package names as entirely separate operators, it will not automatically upgrade an installation of `storage-based-remediation` to `medik8s-storage-based-remediation`.

## Upgrade Instructions

To upgrade from the old package name to the new one, users must manually migrate their OLM Subscription.

### Step 1: Delete the Old Subscription
Delete the Subscription associated with the old package name. This will not delete your `StorageBasedRemediationConfig` or `StorageBasedRemediation` custom resources.

```bash
# Find the subscription name
kubectl get subscription -n openshift-operators | grep storage-based-remediation

# Delete the subscription
kubectl delete subscription <subscription-name> -n openshift-operators
```

### Step 2: Delete the Old ClusterServiceVersion (CSV)

```bash
# Find the CSV name
kubectl get csv -n openshift-operators | grep storage-based-remediation

# Delete the CSV
kubectl delete csv <csv-name> -n openshift-operators
```

### Step 3: Install the New Package

#### Method A: OpenShift Console (Recommended)
1. Navigate to **Operators** -> **OperatorHub**.
2. Search for **"Medik8s Storage-Based Remediation"**.
3. Click **Install** and follow the wizard, ensuring you select the same namespace as the previous installation.

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
  source: community-operators # or redhat-operators
  sourceNamespace: openshift-marketplace
```

Apply the new subscription:
```bash
kubectl apply -f sbr-new-subscription.yaml
```

### Step 4: Verify the Upgrade
Once the new Subscription is created, OLM will install the new CSV. The operator will start and automatically pick up your existing `StorageBasedRemediationConfig` and `StorageBasedRemediation` resources.

```bash
# Check the new CSV status
kubectl get csv -n openshift-operators | grep medik8s-storage-based-remediation

# Verify the operator pods are running
kubectl get pods -n openshift-operators | grep medik8s-storage-based-remediation
```
