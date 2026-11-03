# k3d cluster name (must match dev/k3d_config.yaml metadata.name).
K3D_CLUSTER_NAME ?= plugin-migrations-test

.PHONY: help
help: ## Display available commands.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make <target>\n\n"} /^[a-zA-Z_-]+:.*##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

##@ Build

.PHONY: frontend
frontend: ## Type-check, test and build the frontend bundle.
	npm run build && npm test

.PHONY: backend
backend: ## Format, vet, test and build the backend.
	cd backend && gofmt -l -w . && go vet ./... && go test ./... && go build -o /dev/null .

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	helm lint charts/plugin-migrations/

.PHONY: image
image: ## Build the frontend bundle and the plugin image.
	npm run build
	docker build -t ghcr.io/openeverest/plugin-migrations:dev .

##@ Cluster

.PHONY: k3d-cluster-up
k3d-cluster-up: ## Create a local k3d cluster for development.
	k3d cluster create --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-down
k3d-cluster-down: ## Delete the local k3d cluster.
	k3d cluster delete --config ./dev/k3d_config.yaml

##@ Tilt Development

.PHONY: dev-up
dev-up: k3d-cluster-up ## Create the k3d cluster and start the Tilt dev environment.
	tilt up -f dev/Tiltfile

.PHONY: dev-down
dev-down: ## Stop the Tilt dev environment (keeps the cluster).
	tilt down -f dev/Tiltfile

.PHONY: dev-destroy
dev-destroy: k3d-cluster-down ## Stop Tilt and delete the k3d cluster.
