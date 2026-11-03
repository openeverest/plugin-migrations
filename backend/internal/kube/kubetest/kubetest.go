// Package kubetest provides Kubernetes test doubles shared across packages.
package kubetest

import (
	"errors"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// NewVersionedClient emulates API-server resourceVersion semantics for
// ConfigMaps, which the v0.30 fake object tracker does not implement.
func NewVersionedClient() *fake.Clientset {
	client := fake.NewSimpleClientset()
	version := 0
	next := func() string { version++; return strconv.Itoa(version) }
	client.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if configMap, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.ConfigMap); ok {
			configMap.ResourceVersion = next()
		}
		return false, nil, nil
	})
	client.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		incoming, ok := action.(k8stesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		if !ok {
			return false, nil, nil
		}
		current, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), incoming.Namespace, incoming.Name)
		if err != nil {
			return false, nil, nil
		}
		if stored, ok := current.(*corev1.ConfigMap); ok && stored.ResourceVersion != incoming.ResourceVersion {
			return true, nil, apierrors.NewConflict(corev1.Resource("configmaps"), incoming.Name, errors.New("stale"))
		}
		incoming.ResourceVersion = next()
		return false, nil, nil
	})
	return client
}
