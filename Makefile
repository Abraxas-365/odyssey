.PHONY: build run worker agent agent-image cluster cluster-down test-kube test-kube-k3d test test-db vet fmt tidy gen check setup up dev down reset logs seed iam-sync iam-provision token smoke web web-install web-check api-types

build:
	go build -o bin/odyssey-api ./cmd/odyssey-api
	go build -o bin/odyssey-worker ./cmd/odyssey-worker
	go build -o bin/odyssey-agent ./cmd/odyssey-agent

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
	gofmt -w agent api cmd internal

tidy:
	go mod tidy

# Regenerate internal/permission from iam/permissions.json, and the agent's
# deepcopy code + CRD manifests from agent/api (tests fail while stale).
gen:
	go generate ./internal/permission/...
	go tool controller-gen object paths=./agent/api/...
	go tool controller-gen crd:crdVersions=v1 rbac:roleName=odyssey-agent paths=./agent/... \
		output:crd:artifacts:config=agent/install/crd output:rbac:artifacts:config=agent/install/rbac

# Regenerate the console's wire types from api/openapi.yaml (web-check fails while stale).
api-types:
	@[ -d frontend/node_modules ] || $(MAKE) web-install
	cd frontend && npm run api:types

# What CI runs.
check:
	@test -z "$$(gofmt -l agent api cmd internal)" || (gofmt -l agent api cmd internal && exit 1)
	go vet ./...
	go test ./...

# Run the API on the host against the compose db/IAMKit (needs .env seeded).
run:
	set -a && . ./.env && set +a && \
	DATABASE_URL=postgres://postgres:$$POSTGRES_PASSWORD@localhost:$${DB_PORT:-5438}/odyssey?sslmode=disable \
	IAMKIT_URL=http://localhost:$${IAMKIT_PORT:-8085} PORT=$${ODYSSEY_PORT:-8095} \
	go run ./cmd/odyssey-api

# Run the worker on the host (outbox delivery, workflows, scheduled tasks).
# CLUSTER=kubernetes deploys to the local k3d cluster (make cluster) instead
# of the in-memory fake.
worker:
	set -a && . ./.env && set +a && \
	DATABASE_URL=postgres://postgres:$$POSTGRES_PASSWORD@localhost:$${DB_PORT:-5438}/odyssey?sslmode=disable \
	ODYSSEY_CLUSTER=$(or $(CLUSTER),fake) ODYSSEY_KUBECONFIG=$(KUBECONFIG_LOCAL) \
	go run ./cmd/odyssey-worker

# ---- Local data plane: a k3d cluster with the agent ----
# The cluster's kubeconfig is written to .dev-secrets/ (your ~/.kube/config is
# not touched). Apps answer at http://<app>.apps.odyssey.localhost:8088.
K3D_CLUSTER      ?= odyssey
KUBECONFIG_LOCAL := $(CURDIR)/.dev-secrets/kubeconfig
APPS_PORT        ?= 8088

cluster:
	@mkdir -p .dev-secrets && chmod 700 .dev-secrets
	@k3d cluster list $(K3D_CLUSTER) >/dev/null 2>&1 || \
		k3d cluster create $(K3D_CLUSTER) --agents 0 -p "$(APPS_PORT):80@loadbalancer" \
			--kubeconfig-update-default=false --kubeconfig-switch-context=false --wait
	@k3d kubeconfig get $(K3D_CLUSTER) > $(KUBECONFIG_LOCAL) && chmod 600 $(KUBECONFIG_LOCAL)
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl apply -f agent/install/crd
	@echo "cluster ready: KUBECONFIG=$(KUBECONFIG_LOCAL); run the agent with 'make agent' (or 'make agent-image' for in-cluster)"

cluster-down:
	k3d cluster delete $(K3D_CLUSTER)
	rm -f $(KUBECONFIG_LOCAL)

# The agent on the host against the k3d cluster, with your credentials
# (restart after Go changes). A single copy, no leader election: scale the
# in-cluster one to 0 first if you ran agent-image.
agent:
	KUBECONFIG=$(KUBECONFIG_LOCAL) ODYSSEY_APPS_URL_PORT=$(APPS_PORT) \
	go run ./cmd/odyssey-agent

# The agent inside the cluster instead (as it is installed in a customer's).
agent-image:
	docker build --target agent -t odyssey-agent:dev .
	k3d image import odyssey-agent:dev -c $(K3D_CLUSTER)
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl apply -k agent/install
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl -n odyssey-system rollout restart deployment/odyssey-agent
	KUBECONFIG=$(KUBECONFIG_LOCAL) kubectl -n odyssey-system rollout status deployment/odyssey-agent --timeout=120s

# End-to-end test of releasekube + the agent's controller against the k3d cluster.
# Adapter + controller e2e in a throwaway k3s (testcontainers; needs Docker).
test-kube:
	ODYSSEY_TEST_KUBE=1 go test -count=1 -timeout 15m -run TestKube -v ./internal/release/adapters/releasekube/

# The same e2e against the long-lived k3d cluster (stop other agents first).
test-kube-k3d:
	ODYSSEY_TEST_KUBECONFIG=$(KUBECONFIG_LOCAL) ODYSSEY_TEST_APPS_PORT=$(APPS_PORT) \
	go test -count=1 -run TestKube -v ./internal/release/adapters/releasekube/

# One-time: signing key + .env with random secrets.
setup:
	@mkdir -p secrets && chmod 700 secrets
	@[ -f secrets/jwt.pem ] || (openssl genrsa -out secrets/jwt.pem 4096 2>/dev/null && chmod 644 secrets/jwt.pem && echo "created secrets/jwt.pem")
	@[ -f .env ] || ( sed \
		-e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$$(openssl rand -hex 16)|" \
		-e "s|^OIDC_HMAC_SECRET=.*|OIDC_HMAC_SECRET=$$(openssl rand -hex 32)|" \
		-e "s|^IAMKIT_ENCRYPTION_KEY=.*|IAMKIT_ENCRYPTION_KEY=$$(openssl rand -base64 32)|" \
		-e "s|^DEMO_PASSWORD=.*|DEMO_PASSWORD=$$(openssl rand -hex 12)|" \
		.env.example > .env && chmod 600 .env && echo "created .env" )

up:
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
