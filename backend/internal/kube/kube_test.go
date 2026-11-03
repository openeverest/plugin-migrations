package kube

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
	"github.com/openeverest/plugin-migrations/backend/internal/kube/kubetest"
	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

const testNamespace = "everest-system"

func testRecord() migration.Record {
	return migration.Record{
		ID: "mig-abc", Name: "orders", Engine: "pcsm",
		Source:  migration.Endpoint{Type: migration.EndpointExternal, DatabaseType: "mongodb", Display: "db1:27017"},
		Target:  migration.Endpoint{Type: migration.EndpointInstance, K8sCluster: "main", Namespace: "shop", Name: "orders", Provider: "percona-server-mongodb"},
		Options: json.RawMessage(`{"includeNamespaces":[]}`), WorkerPort: 2242,
		CreatedAt: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC),
	}
}

func TestStoreRoundTripAndOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	store := NewStore(kubetest.NewVersionedClient(), testNamespace)

	created, err := store.Create(ctx, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, testRecord()); migration.KindOf(err) != migration.KindConflict {
		t.Fatalf("duplicate create error = %v", err)
	}
	got, err := store.Get(ctx, "mig-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "orders" || got.Target.Provider != "percona-server-mongodb" || string(got.Options) != `{"includeNamespaces":[]}` {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	started := time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC)
	got.StartedAt = &started
	if _, err := store.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, created); migration.KindOf(err) != migration.KindConflict {
		t.Fatalf("stale update error = %v", err)
	}

	records, err := store.List(ctx)
	if err != nil || len(records) != 1 || records[0].StartedAt == nil {
		t.Fatalf("list = %+v, %v", records, err)
	}
	if err := store.Delete(ctx, "mig-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "mig-abc"); migration.KindOf(err) != migration.KindNotFound {
		t.Fatalf("get after delete error = %v", err)
	}
}

func TestStoreListSkipsForeignAndCorruptConfigMaps(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	store := NewStore(client, testNamespace)
	if _, err := store.Create(ctx, testRecord()); err != nil {
		t.Fatal(err)
	}
	corrupt := &corev1.ConfigMap{}
	corrupt.Name, corrupt.Namespace = "mig-bad", testNamespace
	corrupt.Labels = map[string]string{labelManagedBy: managedByValue, labelComponent: "migration-record"}
	corrupt.Data = map[string]string{recordKey: "{"}
	foreign := &corev1.ConfigMap{}
	foreign.Name, foreign.Namespace = "unrelated", testNamespace
	for _, configMap := range []*corev1.ConfigMap{corrupt, foreign} {
		if _, err := client.CoreV1().ConfigMaps(testNamespace).Create(ctx, configMap, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := store.List(ctx)
	if err != nil || len(records) != 1 || records[0].ID != "mig-abc" {
		t.Fatalf("list = %+v, %v", records, err)
	}
	if _, err := store.Get(ctx, "unrelated"); migration.KindOf(err) != migration.KindNotFound {
		t.Fatalf("foreign ConfigMap must not be readable as a migration: %v", err)
	}
}

func TestWorkloadsApply(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	store := NewStore(client, testNamespace)
	record, err := store.Create(ctx, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	workloads := NewWorkloads(client, testNamespace, WorkerConfig{NodeSelector: map[string]string{"pool": "migrations"}})
	worker := engine.WorkerSpec{
		Image: "percona/percona-clustersync-mongodb:1.0.0", ImagePullPolicy: "IfNotPresent", Port: 2242, HealthPath: "/metrics",
		Env:         map[string]string{"PCSM_PORT": "2242"},
		SecretData:  map[string][]byte{"source-uri": []byte("mongodb://u:p@h"), "target-ca.crt": []byte("PEM")},
		SecretEnv:   map[string]string{"PCSM_SOURCE_URI": "source-uri"},
		SecretFiles: map[string]string{"target-ca.crt": "target-ca.crt"},
	}
	if err := workloads.Apply(ctx, record, worker); err != nil {
		t.Fatal(err)
	}

	secret, err := client.CoreV1().Secrets(testNamespace).Get(ctx, "mig-abc", metav1.GetOptions{})
	if err != nil || string(secret.Data["source-uri"]) != "mongodb://u:p@h" {
		t.Fatalf("secret = %+v, %v", secret, err)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "ConfigMap" || secret.OwnerReferences[0].Name != "mig-abc" {
		t.Fatalf("secret not owned by the record: %+v", secret.OwnerReferences)
	}
	deployment, err := client.AppsV1().Deployments(testNamespace).Get(ctx, "mig-abc", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	container := pod.Containers[0]
	if *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != "Recreate" {
		t.Fatal("worker must run exactly one replica with Recreate strategy")
	}
	if pod.NodeSelector["pool"] != "migrations" || *pod.AutomountServiceAccountToken || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatalf("unexpected pod spec: %+v", pod)
	}
	var sourceEnv *corev1.EnvVar
	for i := range container.Env {
		if container.Env[i].Name == "PCSM_SOURCE_URI" {
			sourceEnv = &container.Env[i]
		}
	}
	if sourceEnv == nil || sourceEnv.Value != "" || sourceEnv.ValueFrom.SecretKeyRef.Key != "source-uri" {
		t.Fatalf("source URI must come from the Secret: %+v", sourceEnv)
	}
	if len(container.VolumeMounts) != 1 || container.VolumeMounts[0].MountPath != engine.SecretMountPath {
		t.Fatalf("CA not mounted: %+v", container.VolumeMounts)
	}
	if items := pod.Volumes[0].Secret.Items; len(items) != 1 || items[0].Key != "target-ca.crt" {
		t.Fatalf("only secret files may be projected: %+v", items)
	}
	if container.ReadinessProbe.HTTPGet.Path != "/metrics" {
		t.Fatal("probe must use the engine health path")
	}

	service, err := client.CoreV1().Services(testNamespace).Get(ctx, "mig-abc", metav1.GetOptions{})
	if err != nil || service.Spec.Ports[0].Port != 2242 || service.Spec.Selector[labelID] != "mig-abc" {
		t.Fatalf("service = %+v, %v", service, err)
	}
	if state, err := workloads.Sync(ctx, "mig-abc"); err != nil || state.Ready || state.Failed {
		t.Fatalf("a new deployment must not be ready yet: %+v %v", state, err)
	}
	if _, err := client.BatchV1().Jobs(testNamespace).Get(ctx, "mig-abc"+bootstrapSuffix, metav1.GetOptions{}); err == nil {
		t.Fatal("no bootstrap Job may be created without a bootstrap command")
	}
	if got := workloads.BaseURL("mig-abc", 2242); got != "http://mig-abc.everest-system.svc:2242" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestWorkloadsBootstrapGatesWorker(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	record, err := NewStore(client, testNamespace).Create(ctx, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	workloads := NewWorkloads(client, testNamespace, WorkerConfig{})
	worker := engine.WorkerSpec{
		Image: "pcsm:1", ImagePullPolicy: "IfNotPresent", Port: 2242, HealthPath: "/metrics",
		SecretData: map[string][]byte{"target-uri": []byte("mongodb://t")},
		SecretEnv:  map[string]string{"PCSM_TARGET_URI": "target-uri"},
		Bootstrap:  &engine.Command{Command: []string{"/usr/bin/pcsm"}, Args: []string{"reset"}},
	}
	if err := workloads.Apply(ctx, record, worker); err != nil {
		t.Fatal(err)
	}
	jobs := client.BatchV1().Jobs(testNamespace)
	deployments := client.AppsV1().Deployments(testNamespace)
	job, err := jobs.Get(ctx, "mig-abc-bootstrap", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Command[0] != "/usr/bin/pcsm" || container.Args[0] != "reset" || container.Env[0].ValueFrom.SecretKeyRef.Key != "target-uri" {
		t.Fatalf("bootstrap container = %+v", container)
	}
	if job.OwnerReferences[0].Name != "mig-abc" || job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("bootstrap job = %+v", job.ObjectMeta)
	}

	replicas := func() int32 {
		deployment, err := deployments.Get(ctx, "mig-abc", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return *deployment.Spec.Replicas
	}
	if replicas() != 0 {
		t.Fatal("worker must wait for the bootstrap")
	}
	if state, _ := workloads.Sync(ctx, "mig-abc"); state.Message != "Preparing the target" || replicas() != 0 {
		t.Fatalf("running bootstrap: %+v replicas %d", state, replicas())
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if _, err := jobs.UpdateStatus(ctx, job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := workloads.Sync(ctx, "mig-abc"); err != nil || replicas() != 1 {
		t.Fatalf("worker not scaled after bootstrap: %v replicas %d", err, replicas())
	}

	// A crashed worker scaled to zero by someone else must not re-run the bootstrap.
	if err := jobs.Delete(ctx, "mig-abc-bootstrap", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if state, err := workloads.Sync(ctx, "mig-abc"); err != nil || state.Failed {
		t.Fatalf("running worker must not depend on the bootstrap Job: %+v %v", state, err)
	}
}

func TestTargetHasWorkers(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	record, err := NewStore(client, testNamespace).Create(ctx, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	workloads := NewWorkloads(client, testNamespace, WorkerConfig{})
	worker := engine.WorkerSpec{Image: "pcsm:1", ImagePullPolicy: "IfNotPresent", Port: 2242, HealthPath: "/metrics",
		Bootstrap: &engine.Command{Args: []string{"reset"}}}
	if err := workloads.Apply(ctx, record, worker); err != nil {
		t.Fatal(err)
	}
	deployment, _ := client.AppsV1().Deployments(testNamespace).Get(ctx, "mig-abc", metav1.GetOptions{})
	job, _ := client.BatchV1().Jobs(testNamespace).Get(ctx, "mig-abc-bootstrap", metav1.GetOptions{})

	target := everest.InstanceRef{K8sCluster: "main", Namespace: "shop", Name: "orders"}
	other := everest.InstanceRef{K8sCluster: "main", Namespace: "shop", Name: "invoices"}
	addPod := func(name string, labels map[string]string, phase corev1.PodPhase) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels}, Status: corev1.PodStatus{Phase: phase}}
		if _, err := client.CoreV1().Pods(testNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	busy := func(ref everest.InstanceRef) bool {
		got, err := workloads.TargetHasWorkers(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	addPod("bootstrap", job.Spec.Template.Labels, corev1.PodSucceeded)
	if busy(target) {
		t.Fatal("a finished bootstrap pod must not block the target")
	}
	addPod("worker", deployment.Spec.Template.Labels, corev1.PodRunning)
	if !busy(target) {
		t.Fatal("a running worker pod must block its target")
	}
	if busy(other) {
		t.Fatal("a worker must not block other targets")
	}
}

func TestWorkloadsBootstrapFailure(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	record, _ := NewStore(client, testNamespace).Create(ctx, testRecord())
	workloads := NewWorkloads(client, testNamespace, WorkerConfig{})
	worker := engine.WorkerSpec{Image: "pcsm:1", ImagePullPolicy: "IfNotPresent", Port: 2242, HealthPath: "/metrics",
		Bootstrap: &engine.Command{Args: []string{"reset"}}}
	if err := workloads.Apply(ctx, record, worker); err != nil {
		t.Fatal(err)
	}
	job, _ := client.BatchV1().Jobs(testNamespace).Get(ctx, "mig-abc-bootstrap", metav1.GetOptions{})
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	if _, err := client.BatchV1().Jobs(testNamespace).UpdateStatus(ctx, job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	state, err := workloads.Sync(ctx, "mig-abc")
	if err != nil || !state.Failed || state.Ready {
		t.Fatalf("state = %+v, %v", state, err)
	}
}
