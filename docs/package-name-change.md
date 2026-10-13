# Package Name Change

## Overview

Starting from v5.8.0, the package name for the Storage Based Remediation community operator has been changed from `storage-based-remediation` to `medik8s-storage-based-remediation`.

## Reason for Change

On an OpenShift cluster, there are multiple catalogSources where each includes  
a different catalog with a set of OLM operators. The Medik8s operators have been
released to the Red Hat catalog and the [Community OKD](https://okd.io/) catalog 
using the same OLM package name. OLM and tooling such as `packagemanifests`
can't reliably distinguish between the operators' variants apart (each from a different
catalog), even if they share the same name, CRDs, etc. Prefixing the package name 
with `medik8s-` makes it unique, so it can always be unambiguously resolved to
our community operator regardless of which catalog source it's queried from.

## Breaking Change

This is a **breaking change** for users of `storage-based-remediation` via OLM. Because OLM treats different package names as entirely separate operators, there is no automatic path from `storage-based-remediation` to `medik8s-storage-based-remediation` — the old operator must be uninstalled and the new one installed fresh.

## Migration Instructions

Moving from the old package name to the new one requires manually uninstalling the old OLM Subscription/CSV and installing a fresh Subscription for the new package, as described below.

**Note -** Before starting this migration, make sure there is no active remediation in progress. Doing this during remediation can leave a node in an inconsistent state.

### Step 1: Delete the Old Subscription

```bash
# Find the subscription name
oc get subscription -n openshift-workload-availability | grep storage-based-remediation

# Delete the subscription
oc delete subscription <subscription-name> -n openshift-workload-availability 
```

### Step 2: Delete the Old ClusterServiceVersion (CSV)

```bash
# Find the CSV name
oc get csv -n openshift-workload-availability  | grep storage-based-remediation

# Delete the CSV
oc delete csv <csv-name> -n openshift-workload-availability 
```

### Step 3: Install the New Package

Log in as a user with `cluster-admin` privileges before proceeding.

#### Method A: OpenShift Console (Recommended)
1. Navigate to **Ecosystem** -> **Software Catalog**.
2. Search for **"Medik8s Storage-Based Remediation"**.
3. The search is matched on display name/keywords, so results from other catalog sources may also appear. Confirm you select the tile sourced from the **community-operators** catalog — verify **Package name: `medik8s-storage-based-remediation`** before installing.
4. Click **Install**, keeping the default installation mode/namespace so the Operator lands in the same namespace as the previous installation.
5. To confirm the install succeeded, go to **Ecosystem** -> **Installed Operators** and check that its status is **Succeeded**. If not, check the **Status** column for errors and inspect the controller-manager/agent pod logs in that namespace.

#### Method B: CLI (Subscription YAML)
Create a new Subscription using the new package name.

**Example Subscription (`sbr-new-subscription.yaml`):**
```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: medik8s-storage-based-remediation-sub
  namespace: openshift-marketplace
spec:
  channel: stable
  name: medik8s-storage-based-remediation
  source: community-operators 
  sourceNamespace: openshift-marketplace
```

Apply the new subscription:
```bash
oc apply -f sbr-new-subscription.yaml
```

### Step 4: Verify the New Installation
Once the new Subscription is created, OLM will install the new CSV.

```bash
# Check the new CSV status
oc get csv -n openshift-workload-availability | grep medik8s-storage-based-remediation

# Verify the operator pods are running
oc get pods -n openshift-workload-availability | grep medik8s-storage-based-remediation
