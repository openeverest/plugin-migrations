package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

const (
	workerContainer    = "worker"
	bootstrapContainer = "bootstrap"
	secretsVolumeName  = "endpoint-secrets"
	bootstrapSuffix    = "-bootstrap"
)

// WorkerConfig comes from Helm values and applies to every worker.
type WorkerConfig struct {
	Resources        corev1.ResourceRequirements
	NodeSelector     map[string]string
	Tolerations      []corev1.Toleration
	ImagePullSecrets []corev1.LocalObjectReference
}

// Workloads runs one Deployment + Service + Secret (+ optional bootstrap Job)
// per migration, all owned by the migration's ConfigMap.
type Workloads struct {
	client    kubernetes.Interface
	namespace string
	config    WorkerConfig
}

func NewWorkloads(client kubernetes.Interface, namespace string, config WorkerConfig) *Workloads {
	return &Workloads{client: client, namespace: namespace, config: config}
}

func workerLabels(record migration.Record, component string) map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelID:        record.ID,
		labelEngine:    record.Engine,
		labelComponent: component,
		labelTarget:    targetLabelValue(record.Target.K8sCluster, record.Target.Namespace, record.Target.Name),
	}
}

// targetLabelValue hashes the target reference so that any instance name fits
// the 63-character label value limit.
func targetLabelValue(k8sCluster, namespace, name string) string {
	sum := sha256.Sum256([]byte(k8sCluster + "/" + namespace + "/" + name))
	return hex.EncodeToString(sum[:])[:40]
}

func selectorLabels(id string) map[string]string {
	return map[string]string{labelID: id, labelComponent: "migration-worker"}
}

// Apply creates the worker objects once per migration; they are immutable
// afterwards because credentials are snapshotted. With a bootstrap, the
// Deployment starts at zero replicas and Sync scales it up after the Job.
func (w *Workloads) Apply(ctx context.Context, record migration.Record, worker engine.WorkerSpec) error {
	if err := worker.Validate(); err != nil {
		return err
	}
	owner, err := w.client.CoreV1().ConfigMaps(w.namespace).Get(ctx, record.ID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get migration record owner: %w", err)
	}
	ownerRefs := []metav1.OwnerReference{{
		APIVersion: "v1", Kind: "ConfigMap", Name: owner.Name, UID: owner.UID,
		Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
	}}
	meta := metav1.ObjectMeta{Name: record.ID, Namespace: w.namespace, Labels: workerLabels(record, "migration-worker"), OwnerReferences: ownerRefs}

	secret := &corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeOpaque, Data: worker.Clone().SecretData}
	if _, err := w.client.CoreV1().Secrets(w.namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create worker secret: %w", err)
	}
	service := &corev1.Service{
		ObjectMeta: meta,
		Spec: corev1.ServiceSpec{
			Selector: selectorLabels(record.ID),
			Ports: []corev1.ServicePort{{
				Name: "control", Port: worker.Port, TargetPort: intstr.FromInt32(worker.Port), Protocol: corev1.ProtocolTCP,
			}},
		},
	}
	if _, err := w.client.CoreV1().Services(w.namespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create worker service: %w", err)
	}

	replicas := int32(1)
	if worker.Bootstrap != nil {
		replicas = 0
		jobMeta := meta
		jobMeta.Name = record.ID + bootstrapSuffix
		jobMeta.Labels = workerLabels(record, "migration-bootstrap")
		if _, err := w.client.BatchV1().Jobs(w.namespace).Create(ctx, w.bootstrapJob(record, jobMeta, worker), metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create worker bootstrap job: %w", err)
		}
	}
	if _, err := w.client.AppsV1().Deployments(w.namespace).Create(ctx, w.deployment(record, meta, worker, replicas), metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create worker deployment: %w", err)
	}
	return nil
}

// container builds the container shared by the Deployment and the bootstrap
// Job: same image, env, secrets and hardening.
func (w *Workloads) container(record migration.Record, name string, worker engine.WorkerSpec) corev1.Container {
	env := make([]corev1.EnvVar, 0, len(worker.Env)+len(worker.SecretEnv))
	for _, envName := range slices.Sorted(maps.Keys(worker.Env)) {
		env = append(env, corev1.EnvVar{Name: envName, Value: worker.Env[envName]})
	}
	for _, envName := range slices.Sorted(maps.Keys(worker.SecretEnv)) {
		env = append(env, corev1.EnvVar{Name: envName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: record.ID}, Key: worker.SecretEnv[envName],
		}}})
	}
	var mounts []corev1.VolumeMount
	if len(worker.SecretFiles) > 0 {
		mounts = append(mounts, corev1.VolumeMount{Name: secretsVolumeName, MountPath: engine.SecretMountPath, ReadOnly: true})
	}
	return corev1.Container{
		Name:            name,
		Image:           worker.Image,
		ImagePullPolicy: corev1.PullPolicy(worker.ImagePullPolicy),
		Env:             env,
		VolumeMounts:    mounts,
		Resources:       *w.config.Resources.DeepCopy(),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

func (w *Workloads) podSpec(record migration.Record, worker engine.WorkerSpec, container corev1.Container) corev1.PodSpec {
	var volumes []corev1.Volume
	if len(worker.SecretFiles) > 0 {
		items := make([]corev1.KeyToPath, 0, len(worker.SecretFiles))
		for _, key := range slices.Sorted(maps.Keys(worker.SecretFiles)) {
			items = append(items, corev1.KeyToPath{Key: key, Path: worker.SecretFiles[key]})
		}
		volumes = append(volumes, corev1.Volume{Name: secretsVolumeName, VolumeSource: corev1.VolumeSource{
			// Files are owned by root and workers run as their image's own
			// non-root user, which the plugin does not know. Only this pod
			// mounts the volume, so world-readable means readable by the worker.
			Secret: &corev1.SecretVolumeSource{SecretName: record.ID, Items: items, DefaultMode: ptr.To[int32](0o444)},
		}})
	}
	return corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		NodeSelector:                 maps.Clone(w.config.NodeSelector),
		Tolerations:                  slices.Clone(w.config.Tolerations),
		ImagePullSecrets:             slices.Clone(w.config.ImagePullSecrets),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Volumes:    volumes,
		Containers: []corev1.Container{container},
	}
}

func (w *Workloads) deployment(record migration.Record, meta metav1.ObjectMeta, worker engine.WorkerSpec, replicas int32) *appsv1.Deployment {
	container := w.container(record, workerContainer, worker)
	container.Ports = []corev1.ContainerPort{{Name: "control", ContainerPort: worker.Port, Protocol: corev1.ProtocolTCP}}
	probe := func(initialDelay, period int32) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: worker.HealthPath, Port: intstr.FromInt32(worker.Port)}},
			InitialDelaySeconds: initialDelay, PeriodSeconds: period, FailureThreshold: 6,
		}
	}
	container.ReadinessProbe = probe(5, 10)
	container.LivenessProbe = probe(30, 20)

	return &appsv1.Deployment{
		ObjectMeta: meta,
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels(record.ID)},
			// Never run two workers for one migration, even during a rollout.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(meta.Labels)},
				Spec:       w.podSpec(record, worker, container),
			},
		},
	}
}

func (w *Workloads) bootstrapJob(record migration.Record, meta metav1.ObjectMeta, worker engine.WorkerSpec) *batchv1.Job {
	container := w.container(record, bootstrapContainer, worker)
	container.Command = slices.Clone(worker.Bootstrap.Command)
	container.Args = slices.Clone(worker.Bootstrap.Args)
	spec := w.podSpec(record, worker, container)
	spec.RestartPolicy = corev1.RestartPolicyNever
	return &batchv1.Job{
		ObjectMeta: meta,
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To[int32](3),
			ActiveDeadlineSeconds: ptr.To[int64](600),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(meta.Labels)},
				Spec:       spec,
			},
		},
	}
}

// Sync scales the worker up once its bootstrap Job succeeded and reports the
// rollout state. Scaling happens only from zero, so worker restarts never
// re-run the bootstrap.
func (w *Workloads) Sync(ctx context.Context, id string) (migration.WorkerState, error) {
	deployment, err := w.client.AppsV1().Deployments(w.namespace).Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		return migration.WorkerState{}, err
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
		job, err := w.client.BatchV1().Jobs(w.namespace).Get(ctx, id+bootstrapSuffix, metav1.GetOptions{})
		if err != nil {
			return migration.WorkerState{}, err
		}
		switch {
		case jobCondition(job, batchv1.JobFailed):
			return migration.WorkerState{Failed: true, Message: fmt.Sprintf("Preparing the target failed; see the logs of Job %s", job.Name)}, nil
		case !jobCondition(job, batchv1.JobComplete):
			return migration.WorkerState{Message: "Preparing the target"}, nil
		}
		deployment.Spec.Replicas = ptr.To[int32](1)
		if _, err := w.client.AppsV1().Deployments(w.namespace).Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			return migration.WorkerState{}, fmt.Errorf("start worker after bootstrap: %w", err)
		}
		return migration.WorkerState{Message: "Starting the migration worker"}, nil
	}
	if deployment.Status.AvailableReplicas > 0 {
		return migration.WorkerState{Ready: true}, nil
	}
	return migration.WorkerState{Message: "Waiting for the migration worker to become available"}, nil
}

// TargetHasWorkers reports whether any worker or bootstrap pod for the target
// still runs, including pods of deleted migrations that are shutting down.
func (w *Workloads) TargetHasWorkers(ctx context.Context, target everest.InstanceRef) (bool, error) {
	selector := labels.SelectorFromSet(labels.Set{
		labelManagedBy: managedByValue,
		labelTarget:    targetLabelValue(target.K8sCluster, target.Namespace, target.Name),
	})
	pods, err := w.client.CoreV1().Pods(w.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return false, fmt.Errorf("list worker pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return true, nil
		}
	}
	return false, nil
}

func jobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Type == conditionType && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// BaseURL relies on the pod's DNS search path, which keeps it independent of
// the cluster domain.
func (w *Workloads) BaseURL(id string, port int32) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", id, w.namespace, port)
}
