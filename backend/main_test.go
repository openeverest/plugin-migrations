package main

import (
	"strings"
	"testing"
)

func TestEngineRegistryFromEnv(t *testing.T) {
	tests := []struct {
		name, env, wantErr string
	}{
		{"enabled pcsm", `{"pcsm":{"enabled":true,"image":{"repository":"percona/percona-clustersync-mongodb","tag":"1.0.0"}}}`, ""},
		{"none enabled", `{"pcsm":{"enabled":false}}`, "at least one enabled engine"},
		{"unset", ``, "at least one enabled engine"},
		{"not compiled", `{"dms":{"enabled":true,"image":{"repository":"x","tag":"1"}}}`, "not compiled"},
		{"missing tag", `{"pcsm":{"enabled":true,"image":{"repository":"x"}}}`, "requires image.repository and image.tag"},
		{"typo", `{"pcsm":{"enabeld":true}}`, "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ENGINES", test.env)
			registry, err := engineRegistryFromEnv()
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				registration, ok := registry.Lookup("pcsm")
				if !ok || registration.Runner.Image != "percona/percona-clustersync-mongodb:1.0.0" || registration.Runner.ImagePullPolicy != "IfNotPresent" {
					t.Fatalf("registration = %+v", registration)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestWorkerConfigFromEnv(t *testing.T) {
	t.Setenv("WORKER_RESOURCES", `{"requests":{"cpu":"500m","memory":"512Mi"}}`)
	t.Setenv("WORKER_TOLERATIONS", `[{"key":"dedicated","operator":"Equal","value":"migrations","effect":"NoSchedule"}]`)
	config, err := workerConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Resources.Requests.Cpu().String() != "500m" || config.Tolerations[0].Value != "migrations" {
		t.Fatalf("config = %+v", config)
	}
	t.Setenv("WORKER_NODE_SELECTOR", `["not","a","map"]`)
	if _, err := workerConfigFromEnv(); err == nil {
		t.Fatal("expected invalid node selector error")
	}
}
