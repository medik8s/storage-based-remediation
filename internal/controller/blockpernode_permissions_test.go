package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockpernode"
)

type permissionCreateClient struct {
	client.Client
	failKind string
	err      error
	creates  []string
}

func (c *permissionCreateClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	var kind string
	switch object.(type) {
	case *corev1.ServiceAccount:
		kind = "ServiceAccount"
	case *rbacv1.ClusterRoleBinding:
		kind = "ClusterRoleBinding"
	}
	c.creates = append(c.creates, kind)
	if kind == c.failKind {
		return c.err
	}
	return c.Client.Create(ctx, object, options...)
}

func TestPerNodePermissionsCreation(t *testing.T) {
	for _, entry := range []struct {
		name                string
		resourceName        func(*api.StorageBasedRemediationConfig) string
		ensure              func(*StorageBasedRemediationConfigReconciler, context.Context, *api.StorageBasedRemediationConfig) error
		role                string
		serviceAccountError string
		bindingError        string
	}{
		{"watcher", watcherResourceName, (*StorageBasedRemediationConfigReconciler).ensureWatcherPermissions,
			"sbr-operator-sbr-watcher-role", "watcher service account belongs to another owner", "watcher binding belongs to another owner"},
		{"peer", peerResourceName, (*StorageBasedRemediationConfigReconciler).ensurePeerPermissions,
			"sbr-operator-sbr-agent-role", "peer service account identity does not match", "peer binding identity does not match"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			ctx := context.Background()
			config := watcherConfig()
			name := entry.resourceName(config)
			managed := map[string]string{blockpernode.ConfigurationUIDLabel: string(config.UID), "app.kubernetes.io/managed-by": SBROperatorName}

			t.Run("create-and-repeat", func(t *testing.T) {
				r := watcherReconciler()
				for call := 0; call < 2; call++ {
					if err := entry.ensure(r, ctx, config); err != nil {
						t.Fatal(err)
					}
					sa := &corev1.ServiceAccount{}
					if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: name}, sa); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(sa.Labels, managed) {
						t.Fatalf("service account labels = %v, want %v", sa.Labels, managed)
					}
					binding := &rbacv1.ClusterRoleBinding{}
					if err := r.Get(ctx, client.ObjectKey{Name: name}, binding); err != nil {
						t.Fatal(err)
					}
					role := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: entry.role}
					subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: config.Namespace}}
					if !reflect.DeepEqual(binding.Labels, managed) || binding.RoleRef != role || !reflect.DeepEqual(binding.Subjects, subjects) {
						t.Fatalf("binding labels, role, or subjects changed: %+v", binding)
					}
				}
			})

			for _, resource := range []string{"service-account", "binding"} {
				for _, owner := range []string{"", "other-configuration"} {
					t.Run(resource+"-owner-"+owner, func(t *testing.T) {
						labels := map[string]string{"unrelated": "preserved"}
						if owner != "" {
							labels[blockpernode.ConfigurationUIDLabel] = owner
						}
						metadata := metav1.ObjectMeta{Name: name, ResourceVersion: "1", Labels: labels}
						var foreign client.Object
						wantError := entry.bindingError
						if resource == "service-account" {
							metadata.Namespace = config.Namespace
							foreign = &corev1.ServiceAccount{ObjectMeta: metadata}
							wantError = entry.serviceAccountError
						} else {
							foreign = &rbacv1.ClusterRoleBinding{ObjectMeta: metadata,
								RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "unrelated"},
								Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "unrelated", Namespace: "other"}}}
						}
						r := watcherReconciler(foreign)
						if err := r.Get(ctx, client.ObjectKeyFromObject(foreign), foreign); err != nil {
							t.Fatal(err)
						}
						before := foreign.DeepCopyObject()
						if err := entry.ensure(r, ctx, config); err == nil || err.Error() != wantError {
							t.Fatalf("ownership error = %v, want %q", err, wantError)
						}
						if err := r.Get(ctx, client.ObjectKeyFromObject(foreign), foreign); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(foreign, before) {
							t.Fatal("controller changed the foreign resource")
						}
						if resource == "service-account" {
							if err := r.Get(ctx, client.ObjectKey{Name: name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
								t.Fatalf("service account failure did not prevent binding creation: %v", err)
							}
						} else {
							sa := &corev1.ServiceAccount{}
							if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: name}, sa); err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(sa.Labels, managed) {
								t.Fatal("binding failure changed the successful service account")
							}
						}
					})
				}
			}

			for _, failedKind := range []string{"ServiceAccount", "ClusterRoleBinding"} {
				t.Run("create-error-"+failedKind, func(t *testing.T) {
					r := watcherReconciler()
					failure := errors.New("permission creation failed")
					c := &permissionCreateClient{Client: r.Client, failKind: failedKind, err: failure}
					r.Client = c
					if err := entry.ensure(r, ctx, config); !errors.Is(err, failure) {
						t.Fatalf("create error = %v, want %v", err, failure)
					}
					wantCreates := []string{"ServiceAccount"}
					if failedKind == "ClusterRoleBinding" {
						wantCreates = append(wantCreates, "ClusterRoleBinding")
						sa := &corev1.ServiceAccount{}
						if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: name}, sa); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(sa.Labels, managed) {
							t.Fatal("binding failure changed the successful service account")
						}
					}
					if !reflect.DeepEqual(c.creates, wantCreates) {
						t.Fatalf("create operations = %v, want %v", c.creates, wantCreates)
					}
				})
			}
		})
	}
}
