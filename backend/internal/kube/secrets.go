package kube

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SecretReader reads Secrets in any namespace the ServiceAccount may read
// (the chart grants this only when an engine needs backup storages).
type SecretReader struct {
	client kubernetes.Interface
}

func NewSecretReader(client kubernetes.Interface) *SecretReader {
	return &SecretReader{client: client}
}

func (r *SecretReader) ReadSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	secret, err := r.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}
