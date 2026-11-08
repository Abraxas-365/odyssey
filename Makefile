.PHONY: access-key build run worker gateway agent agent-image agent-enroll cluster cluster-addons cluster-down test-kube test-kube-k3d test-build test-cnpg test test-db vet fmt tidy gen check setup up dev down reset logs seed iam-sync iam-provision token smoke web web-install web-check api-types github-app

# The Railpack library's version names the plans the builder writes (the
# Dockerfile sets it the same way).
RAILPACK_LDFLAGS = -X github.com/Abraxas-365/odyssey/agent/builder.RailpackVersion=$(patsubst v%,%,$(shell go list -m -f '{{.Version}}' github.com/railwayapp/railpack))

build:
	go build -o bin/odyssey-api ./cmd/odyssey-api
	go build -o bin/odyssey-worker ./cmd/odyssey-worker
	go build -o bin/odyssey-gateway ./cmd/odyssey-gateway
	go build -ldflags="$(RAILPACK_LDFLAGS)" -o bin/odyssey-agent ./cmd/odyssey-agent

test:
	go test ./...

# Unit tests plus Postgres tests (pgtest) against the compose database: each
# test creates and drops its own throwaway database. Without it, plain
# `go test` starts a Postgres container per package (testcontainers).
test-db:
	@set -a && . ./.env && set +a && \
	ODYSSEY_TEST_DATABASE_URL=postgres://postgres:$$POSTGRES_PASSWORD@localhost:$${DB_PORT:-5438}/postgres?sslmode=disable \
	ODYSSEY_TEST_REQUIRE_DB=1 go test $(or $(PKG),./...)

vet:
	go vet ./...

fmt:
	gofmt -w agent api cmd internal pkg proto

tidy:
	go mod tidy

# Regenerate internal/permission from iam/permissions.json, the fleet
# protocol from proto/ (needs protoc; the Go plugins are go tools), and the
# agent's deepcopy code + CRD manifests from agent/api (tests fail while stale).
gen:
	go generate ./internal/permission/...
	protoc --plugin=protoc-gen-go="$$(go tool -n protoc-gen-go)" --plugin=protoc-gen-go-grpc="$$(go tool -n protoc-gen-go-grpc)" \
		--go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/fleet/v1/fleet.proto
	go tool controller-gen object paths=./agent/api/...
	go tool controller-gen crd:crdVersions=v1 rbac:roleName=odyssey-agent paths=./agent/... \
		output:crd:artifacts:config=agent/install/crd output:rbac:artifacts:config=agent/install/rbac

# Regenerate the console's wire types from api/openapi.yaml (web-check fails while stale).
api-types:
	@[ -d frontend/node_modules ] || $(MAKE) web-install
	cd frontend && npm run api:types

# What CI runs.
check:
	@test -z "$$(gofmt -l agent api cmd internal pkg proto)" || (gofmt -l agent api cmd internal pkg proto && exit 1)
	go vet ./...
	go test ./...

# The key the API signs access tickets with (apps protected with an Odicea
# login): an Ed25519 seed, base64, generated once in .dev-secrets/ (never
# committed; 644 like secrets/jwt.pem so the API container's user can read the
# bind mount, inside a 700 directory). Production sets ODYSSEY_ACCESS_SIGNING_KEY[_FILE] itself.
ACCESS_KEY := $(CURDIR)/.dev-secrets/access-signing.key
access-key:
	@mkdir -p .dev-secrets && chmod 700 .dev-secrets
	@[ -f $(ACCESS_KEY) ] || (openssl rand -base64 32 > $(ACCESS_KEY) && chmod 644 $(ACCESS_KEY) && echo "created $(ACCESS_KEY)")

# Run the API on the host against the compose db/IAMKit (needs .env seeded).
# It registers clusters (tokens point agents at the local gateway). CLUSTER
# and BUILDER must match the worker's: the API reads live build logs
# through the same builder.
run: access-key
	set -a && . ./.env && set +a && \
	ODYSSEY_ACCESS_SIGNING_KEY_FILE=$(ACCESS_KEY) \
	DATABASE_URL=postgres://postgres:$$POSTGRES_PASSWORD@localhost:$${DB_PORT:-5438}/odyssey?sslmode=disable \
	IAMKIT_URL=http://localhost:$${IAMKIT_PORT:-8085} PORT=$${ODYSSEY_PORT:-8095} \
	FLEET_CA_CERT=$(FLEET_DIR)/ca.crt FLEET_GATEWAY_URL=$(GATEWAY_HOST):$(GATEWAY_PORT) \
	ODYSSEY_CLUSTER=$(or $(CLUSTER),fake) ODYSSEY_BUILDER='$(BUILDER)' \
	ODYSSEY_BLOB_URL='$(BLOB_URL)' \
	go run ./cmd/odyssey-api

# Register Odyssey's GitHub App through GitHub's manifest flow: opens the
# browser, then writes the secrets to .dev-secrets/github/ (github.env has the
# variables to load). Flags via ARGS, e.g. ARGS="--org acme --public-url https://odyssey.example".
github-app:
	go run ./cmd/odyssey-api github-app create $(ARGS)

# Run the worker on the host (outbox delivery, workflows, scheduled tasks).
# CLUSTER=fleet deploys to the organization's enrolled clusters (make
# gateway + make agent-enroll) instead of the in-memory fake. BUILDER picks
# who builds uploaded sources (fake: a scripted log and a placeholder image;
# fleet: the cluster's builder, the default with CLUSTER=fleet; none: builds
# fail with NO_BUILDER).
worker:
	set -a && . ./.env && set +a && \
	DATABASE_URL=postgres://postgres:$$POSTGRES_PASSWORD@localhost:$${DB_PORT:-5438}/odyssey?sslmode=disable \
	IAMKIT_URL=http://localhost:$${IAMKIT_PORT:-8085} \
	ODYSSEY_CLUSTER=$(or $(CLUSTER),fake) ODYSSEY_BUILDER='$(BUILDER)' \
	ODYSSEY_BLOB_URL='$(BLOB_URL)' \
	go run ./cmd/odyssey-worker

# Uploaded sources and build logs on the host (make run and make worker
# share it; the compose services share the blobs volume instead).
BLOB_URL := file://$(CURDIR)/.data/blobs?create_dir=1&no_tmp_dir=1

# ---- Fleet: the gateway agents connect to ----
# The fleet CA (make setup) lives in secrets/fleet/; only the gateway holds
# its key. Agents in k3d reach the host as host.k3d.internal.
FLEET_DIR    := $(CURDIR)/secrets/fleet
GATEWAY_HOST ?= host.k3d.internal
GATEWAY_PORT ?= 8443

gateway:
	set -a && . ./.env && set +a && \
	DATABASE_URL=postgres://postgres:$$POSTGRES_PASSWORD@localhost:$${DB_PORT:-5438}/odyssey?sslmode=disable \
	FLEET_CA_CERT=$(FLEET_DIR)/ca.crt FLEET_CA_KEY=$(FLEET_DIR)/ca.key \
	FLEET_GATEWAY_LISTEN=:$(GATEWAY_PORT) FLEET_GATEWAY_HOSTS=$(GATEWAY_HOST),localhost \
	ODYSSEY_BLOB_URL='$(BLOB_URL)' \
	go run ./cmd/odyssey-gateway

# ---- Local data plane: a k3d cluster with the agent ----
# The cluster's kubeconfig is written to .dev-secrets/ (your ~/.kube/config is
# not touched). Apps answer at http://<app>.apps.odyssey.localhost:8088.
K3D_CLUSTER      ?= odyssey
K3D_NODE         ?= k3d-$(K3D_CLUSTER)-server-0
KUBECONFIG_LOCAL := $(CURDIR)/.dev-secrets/kubeconfig
APPS_PORT        ?= 8088

# The cluster comes with zot (deploy/k3d/registry), the registry in-cluster
# builds push to. The node's containerd cannot resolve cluster DNS, so
# deploy/k3d/registries.yaml mirrors zot's name to its fixed ClusterIP; k3s
# reads it at start: a new cluster gets it at creation, an existing one has
# it copied in and is stopped and started (state is kept, nothing is
# recreated). Not a bare `docker restart`: only k3d start re-injects
# host.k3d.internal (the gateway's name) and refreshes the API load balancer.
cluster:
	@mkdir -p .dev-secrets && chmod 700 .dev-secrets
	@k3d cluster list $(K3D_CLUSTER) >/dev/null 2>&1 || \
		k3d cluster create $(K3D_CLUSTER) --agents 0 -p "$(APPS_PORT):80@loadbalancer" \
			--registry-config deploy/k3d/registries.yaml \
			--kubeconfig-update-default=false --kubeconfig-switch-context=false --wait
	@k3d cluster start $(K3D_CLUSTER) --wait >/dev/null 2>&1 || true
	@docker exec $(K3D_NODE) cat /etc/rancher/k3s/registries.yaml 2>/dev/null | cmp -s - deploy/k3d/registries.yaml || { \
		echo "installing deploy/k3d/registries.yaml in $(K3D_NODE) and restarting the cluster"; \
		docker cp deploy/k3d/registries.yaml $(K3D_NODE):/etc/rancher/k3s/registries.yaml && \
		k3d cluster stop $(K3D_CLUSTER) && k3d cluster start $(K3D_CLUSTER) --wait; }
	@k3d kubeconfig get $(K3D_CLUSTER) > $(KUBECONFIG_LOCAL) && chmod 600 $(KUBECONFIG_LOCAL)
	@for i in $$(seq 30); do KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl --request-timeout=5s wait --for=condition=Ready node --all --timeout=5s >/dev/null 2>&1 && exit 0; sleep 2; done; \
		echo "the cluster API did not come up" >&2; exit 1
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl apply -f agent/install/crd
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl apply -k deploy/k3d/registry
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl -n odyssey-registry rollout status deployment/zot --timeout=180s
	$(MAKE) cluster-addons
	@echo "cluster ready: KUBECONFIG=$(KUBECONFIG_LOCAL); connect it with 'make agent-enroll NAME=local', then 'make agent' (or 'make agent-image' for in-cluster, which can also build)"

# Operators the cluster's resources need (docs/resources.md): CloudNativePG
# makes the databases. Pinned to a release by version and URL; server-side
# apply because its CRDs are too big for a client-side annotation.
CNPG_VERSION ?= 1.27.4
CNPG_URL     ?= https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v$(CNPG_VERSION)/cnpg-$(CNPG_VERSION).yaml
cluster-addons:
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl apply --server-side -f $(CNPG_URL)
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=180s

cluster-down:
	k3d cluster delete $(K3D_CLUSTER)
	rm -f $(KUBECONFIG_LOCAL)

# Registers the k3d cluster with the platform (as "Conectar clúster" in the
# console does: needs make run + make token's demo user) and stores the
# one-time token in the agent's Secret; the agent enrols with it on start.
agent-enroll:
	KUBECONFIG=$(KUBECONFIG_LOCAL) ./scripts/agent-enroll.sh "$(or $(NAME),local)"

# The agent on the host against the k3d cluster, with your credentials
# (restart after Go changes). A single copy, no leader election: scale the
# in-cluster one to 0 first if you ran agent-image. Its Secret names the
# gateway as k3d sees it; from the host it is dialled on localhost.
agent:
	KUBECONFIG=$(KUBECONFIG_LOCAL) ODYSSEY_APPS_URL_PORT=$(APPS_PORT) \
	ODYSSEY_GATEWAY_DIAL=localhost:$(GATEWAY_PORT) \
	go run -ldflags="$(RAILPACK_LDFLAGS)" ./cmd/odyssey-agent

# The agent inside the cluster instead (as it is installed in a customer's),
# with the local registry (needs make cluster) so it can build. The agent on
# the host (make agent) does not build: build pods could not reach it.
agent-image:
	docker build --target agent -t odyssey-agent:dev .
	k3d image import odyssey-agent:dev -c $(K3D_CLUSTER)
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl apply -k deploy/k3d/agent
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl -n odyssey-system rollout restart deployment/odyssey-agent
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl -n odyssey-system rollout status deployment/odyssey-agent --timeout=120s

# End to end on a real cluster: gateway + agent (in-process) + releasefleet,
# in a throwaway k3s (testcontainers; needs Docker).
test-kube:
	ODYSSEY_TEST_KUBE=1 go test -count=1 -timeout 15m -run '^TestKube$$' -v ./internal/bootstrap/

# The same e2e against the long-lived k3d cluster (stop other agents first),
# then the apps of several services (TestKubeServices: needs the cluster to
# reach the host, like test-build). One agent per process: two runs.
test-kube-k3d:
	ODYSSEY_TEST_KUBECONFIG=$(KUBECONFIG_LOCAL) ODYSSEY_TEST_APPS_PORT=$(APPS_PORT) \
	go test -count=1 -run '^TestKube$$' -v ./internal/bootstrap/
	ODYSSEY_TEST_KUBECONFIG=$(KUBECONFIG_LOCAL) ODYSSEY_TEST_APPS_PORT=$(APPS_PORT) \
	go test -count=1 -timeout 15m -run '^TestKubeServices$$' -v ./internal/bootstrap/

# Real builds on k3d (needs make cluster: zot + its mirror): folders uploaded
# through the API are built by an in-process agent's BuildKit pods, pushed to
# zot and deployed. The test pauses the in-cluster agent while it runs. Run
# it alone: each build takes up to 4 GiB.
test-build:
	docker build --target agent -t odyssey-agent:dev .
	k3d image import odyssey-agent:dev -c $(K3D_CLUSTER)
	ODYSSEY_TEST_KUBECONFIG=$(KUBECONFIG_LOCAL) ODYSSEY_TEST_APPS_PORT=$(APPS_PORT) ODYSSEY_TEST_BUILD=1 \
	go test -count=1 -timeout 20m -run '^TestKubeBuild$$' -v ./internal/bootstrap/

# A recipe's database settings on a real CloudNativePG, in a throwaway k3d
# cluster (created and deleted here): the pinned image, instances, resources,
# a change of instances, a refused major, deletion.
test-cnpg:
	@mkdir -p .dev-secrets && chmod 700 .dev-secrets
	-@k3d cluster delete odyssey-cnpg-test >/dev/null 2>&1
	k3d cluster create odyssey-cnpg-test --agents 0 --no-lb --k3s-arg "--disable=traefik@server:0" \
		--kubeconfig-update-default=false --kubeconfig-switch-context=false --wait && \
	k3d kubeconfig get odyssey-cnpg-test > .dev-secrets/kubeconfig-cnpg-test && chmod 600 .dev-secrets/kubeconfig-cnpg-test && \
	KUBECONFIG=.dev-secrets/kubeconfig-cnpg-test kubectl apply -f agent/install/crd && \
	KUBECONFIG=.dev-secrets/kubeconfig-cnpg-test kubectl apply --server-side -f $(CNPG_URL) && \
	KUBECONFIG=.dev-secrets/kubeconfig-cnpg-test kubectl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=300s && \
	ODYSSEY_TEST_CNPG_KUBECONFIG=$(CURDIR)/.dev-secrets/kubeconfig-cnpg-test \
	go test -count=1 -timeout 15m -run '^TestRecipeSettingsOnRealCloudNativePG$$' -v ./agent/controllers/resource/; \
	s=$$?; k3d cluster delete odyssey-cnpg-test; rm -f .dev-secrets/kubeconfig-cnpg-test; exit $$s

# One-time: signing key, fleet CA + .env with random secrets.
setup:
	@mkdir -p secrets && chmod 700 secrets
	@[ -f secrets/jwt.pem ] || (openssl genrsa -out secrets/jwt.pem 4096 2>/dev/null && chmod 644 secrets/jwt.pem && echo "created secrets/jwt.pem")
	@mkdir -p $(FLEET_DIR) && chmod 700 $(FLEET_DIR)
	@[ -f $(FLEET_DIR)/ca.key ] || go run ./cmd/odyssey-gateway ca init $(FLEET_DIR)/ca.crt $(FLEET_DIR)/ca.key
	@[ -f .env ] || ( sed \
		-e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$$(openssl rand -hex 16)|" \
		-e "s|^OIDC_HMAC_SECRET=.*|OIDC_HMAC_SECRET=$$(openssl rand -hex 32)|" \
		-e "s|^IAMKIT_ENCRYPTION_KEY=.*|IAMKIT_ENCRYPTION_KEY=$$(openssl rand -base64 32)|" \
		-e "s|^DEMO_PASSWORD=.*|DEMO_PASSWORD=$$(openssl rand -hex 12)|" \
		.env.example > .env && chmod 600 .env && echo "created .env" )
	@grep -q '^ODYSSEY_UID=' .env || printf 'ODYSSEY_UID=%s\nODYSSEY_GID=%s\n' "$$(id -u)" "$$(id -g)" >> .env
	@$(MAKE) --no-print-directory access-key

up: access-key
	@if grep -q '^IAMKIT_ENVIRONMENT_ID=.' .env 2>/dev/null; then \
		docker compose up -d --build --wait; \
	else \
		docker compose up -d --build --wait db iamkit && \
		echo "IAMKit is up but not seeded yet: run 'make seed' then 'make up'"; \
	fi

# Full first run: secrets, IAMKit, seed, API.
dev: setup
	$(MAKE) up
	@grep -q '^IAMKIT_ENVIRONMENT_ID=.' .env || $(MAKE) seed
	$(MAKE) up

down:
	docker compose down

# Destroys local data (volumes) and the seeded IAM ids.
reset:
	docker compose down -v
	rm -rf .dev-secrets .data
	@[ ! -f .env ] || sed -i.bak '/written by scripts\/seed-iam.sh/,$$d' .env && rm -f .env.bak

logs:
	docker compose logs -f api worker iamkit

# Provision the local IAMKit from iam/permissions.json and write IAMKIT_* to .env.
seed:
	./scripts/seed-iam.sh

# Push iam/permissions.json changes to the local IAMKit.
# DRY_RUN=1 shows the changes; PRUNE=1 also removes permissions that left the manifest.
iam-sync:
	@./scripts/seed-iam.sh $(if $(DRY_RUN),--dry-run) $(if $(PRUNE),--prune)

# Provision ANY IAMKit (staging/prod): IAMKIT_URL + IAMKIT_KEY(_FILE) + APP_URL/AUDIENCE...
# See scripts/provision-iam.sh --help.
iam-provision:
	@./scripts/provision-iam.sh $(if $(DRY_RUN),--dry-run) $(if $(PRUNE),--prune)

token:
	@./scripts/token.sh

# End-to-end check with a real IAMKit token.
smoke:
	@./scripts/smoke.sh

# Web console (frontend/). npm 10.9 crashes on this tree, so pin npm 11.
web-install:
	cd frontend && npx -y npm@11 install

web:
	@[ -d frontend/node_modules ] || $(MAKE) web-install
	cd frontend && npm run dev

web-check:
	@[ -d frontend/node_modules ] || $(MAKE) web-install
	cd frontend && npm run check
