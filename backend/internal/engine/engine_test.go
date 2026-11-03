package engine

import "testing"

type stubEngine struct{ id string }

func (s stubEngine) ID() string           { return s.id }
func (s stubEngine) Describe() Descriptor { return Descriptor{ID: s.id} }
func (s stubEngine) Supports(source, target EndpointInfo) bool {
	return source.DatabaseType == "mongodb"
}
func (s stubEngine) Prepare(PrepareInput) (Prepared, error) { return Prepared{}, nil }
func (s stubEngine) Controller(string) Controller           { return nil }

var validRunner = RunnerConfig{Image: "img", ImagePullPolicy: "IfNotPresent"}

func TestNewRegistryValidation(t *testing.T) {
	tests := map[string][]Registration{
		"empty":        nil,
		"nil engine":   {{Runner: validRunner}},
		"blank ID":     {{Engine: stubEngine{id: " "}, Runner: validRunner}},
		"duplicate ID": {{Engine: stubEngine{id: "a"}, Runner: validRunner}, {Engine: stubEngine{id: "a"}, Runner: validRunner}},
		"bad runner":   {{Engine: stubEngine{id: "a"}, Runner: RunnerConfig{Image: "img", ImagePullPolicy: "Sometimes"}}},
	}
	for name, registrations := range tests {
		if _, err := NewRegistry(registrations...); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestRegistryResolve(t *testing.T) {
	registry, err := NewRegistry(
		Registration{Engine: stubEngine{id: "b"}, Runner: validRunner},
		Registration{Engine: stubEngine{id: "a"}, Runner: validRunner},
	)
	if err != nil {
		t.Fatal(err)
	}
	if descriptors := registry.Descriptors(); descriptors[0].ID != "a" || descriptors[1].ID != "b" {
		t.Fatalf("descriptors not sorted: %v", descriptors)
	}
	mongo := EndpointInfo{DatabaseType: "mongodb"}
	if _, err := registry.Resolve("a", mongo, mongo); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"missing": "", "unknown": "zzz"} {
		if _, err := registry.Resolve(id, mongo, mongo); KindOf(err) != KindInvalidRequest {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, err := registry.Resolve("a", EndpointInfo{DatabaseType: "postgresql"}, mongo); KindOf(err) != KindInvalidRequest {
		t.Errorf("unsupported pair: error = %v", err)
	}
}

func TestWorkerSpecValidate(t *testing.T) {
	valid := WorkerSpec{
		Image: "img", Port: 2242, HealthPath: "/metrics",
		Env:         map[string]string{"A": "1"},
		SecretData:  map[string][]byte{"uri": []byte("x"), "ca": []byte("y")},
		SecretEnv:   map[string]string{"URI": "uri"},
		SecretFiles: map[string]string{"ca": "ca.crt"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*WorkerSpec){
		"no image":           func(s *WorkerSpec) { s.Image = "" },
		"bad port":           func(s *WorkerSpec) { s.Port = 0 },
		"relative health":    func(s *WorkerSpec) { s.HealthPath = "metrics" },
		"env and secret env": func(s *WorkerSpec) { s.Env["URI"] = "plain" },
		"dangling env ref":   func(s *WorkerSpec) { s.SecretEnv["X"] = "missing" },
		"dangling file ref":  func(s *WorkerSpec) { s.SecretFiles["missing"] = "f" },
		"path in file name":  func(s *WorkerSpec) { s.SecretFiles["ca"] = "../ca.crt" },
	}
	for name, mutate := range mutations {
		spec := valid.Clone()
		mutate(&spec)
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestWorkerSpecCloneIsDeep(t *testing.T) {
	original := WorkerSpec{SecretData: map[string][]byte{"k": []byte("secret")}, Env: map[string]string{"A": "1"}}
	clone := original.Clone()
	clone.SecretData["k"][0] = 'X'
	clone.Env["A"] = "2"
	if string(original.SecretData["k"]) != "secret" || original.Env["A"] != "1" {
		t.Fatal("clone shares mutable state")
	}
}
