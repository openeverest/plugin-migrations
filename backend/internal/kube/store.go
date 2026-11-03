// Package kube implements migration.Store and migration.Workloads on plain
// Kubernetes objects: one ConfigMap per migration owns its Secret, Deployment
// and Service, so deleting the record garbage-collects the whole migration.
package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

const (
	recordKey = "migration.json"

	labelManagedBy = "app.kubernetes.io/managed-by"
	managedByValue = "plugin-migrations"
	labelID        = "migrations.openeverest.io/id"
	labelEngine    = "migrations.openeverest.io/engine"
	labelComponent = "app.kubernetes.io/component"
	labelTarget    = "migrations.openeverest.io/target"
)

// Store keeps migration records in ConfigMaps of one namespace.
type Store struct {
	client    kubernetes.Interface
	namespace string
}

func NewStore(client kubernetes.Interface, namespace string) *Store {
	return &Store{client: client, namespace: namespace}
}

func recordLabels(record migration.Record) map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelID:        record.ID,
		labelEngine:    record.Engine,
		labelComponent: "migration-record",
	}
}

func (s *Store) toConfigMap(record migration.Record) (*corev1.ConfigMap, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode migration record: %w", err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: record.ID, Namespace: s.namespace, Labels: recordLabels(record),
			ResourceVersion: record.ResourceVersion,
		},
		Data: map[string]string{recordKey: string(encoded)},
	}, nil
}

func fromConfigMap(configMap *corev1.ConfigMap) (migration.Record, error) {
	var record migration.Record
	if err := json.Unmarshal([]byte(configMap.Data[recordKey]), &record); err != nil {
		return migration.Record{}, fmt.Errorf("decode migration record %s: %w", configMap.Name, err)
	}
	if record.ID != configMap.Name {
		return migration.Record{}, fmt.Errorf("migration record %s has mismatched id", configMap.Name)
	}
	record.ResourceVersion = configMap.ResourceVersion
	return record, nil
}

func translate(err error, id string) error {
	switch {
	case apierrors.IsNotFound(err):
		return &migration.Error{Kind: migration.KindNotFound, Message: "migration not found"}
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		return &migration.Error{Kind: migration.KindConflict, Message: fmt.Sprintf("migration %s was modified concurrently; retry", id)}
	}
	return err
}

func (s *Store) Create(ctx context.Context, record migration.Record) (migration.Record, error) {
	record.ResourceVersion = ""
	configMap, err := s.toConfigMap(record)
	if err != nil {
		return migration.Record{}, err
	}
	created, err := s.client.CoreV1().ConfigMaps(s.namespace).Create(ctx, configMap, metav1.CreateOptions{})
	if err != nil {
		return migration.Record{}, translate(err, record.ID)
	}
	return fromConfigMap(created)
}

func (s *Store) Get(ctx context.Context, id string) (migration.Record, error) {
	configMap, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		return migration.Record{}, translate(err, id)
	}
	if configMap.Labels[labelManagedBy] != managedByValue {
		return migration.Record{}, &migration.Error{Kind: migration.KindNotFound, Message: "migration not found"}
	}
	return fromConfigMap(configMap)
}

func (s *Store) List(ctx context.Context) ([]migration.Record, error) {
	selector := labels.Set{labelManagedBy: managedByValue, labelComponent: "migration-record"}.String()
	list, err := s.client.CoreV1().ConfigMaps(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	records := make([]migration.Record, 0, len(list.Items))
	for i := range list.Items {
		record, err := fromConfigMap(&list.Items[i])
		if err != nil {
			// One corrupt record must not hide every other migration.
			log.Printf("skip migration record: %v", err)
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) Update(ctx context.Context, record migration.Record) (migration.Record, error) {
	if record.ResourceVersion == "" {
		return migration.Record{}, fmt.Errorf("update of migration %s requires a resource version", record.ID)
	}
	existing, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, record.ID, metav1.GetOptions{})
	if err != nil {
		return migration.Record{}, translate(err, record.ID)
	}
	configMap, err := s.toConfigMap(record)
	if err != nil {
		return migration.Record{}, err
	}
	// Preserve fields this store does not own (e.g. annotations added by tools).
	existing.Data = configMap.Data
	existing.Labels = configMap.Labels
	existing.ResourceVersion = record.ResourceVersion
	updated, err := s.client.CoreV1().ConfigMaps(s.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return migration.Record{}, translate(err, record.ID)
	}
	return fromConfigMap(updated)
}

// Delete removes the record; Kubernetes garbage-collects the owned worker.
func (s *Store) Delete(ctx context.Context, id string) error {
	propagation := metav1.DeletePropagationBackground
	err := s.client.CoreV1().ConfigMaps(s.namespace).Delete(ctx, id, metav1.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil {
		return translate(err, id)
	}
	return nil
}
