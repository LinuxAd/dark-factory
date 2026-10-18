# Dark Factory. Cluster targets need FACTORY_KUBE_CONTEXT set to the platform
# cluster's kubectl context; the scripts refuse to run without it. Bring-up and
# teardown are platform-project's `make up` / `make down`.

SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

IMAGE_TAG ?= local
RUNNER_IMAGE ?= factory-runner:$(IMAGE_TAG)
EGRESS_IMAGE ?= factory-egress:$(IMAGE_TAG)

# Pinned so local and CI runs report the same codes; the runner image's own
# shellcheck is older and flags SC2317 in trap handlers that 0.11 accepts.
SHELLCHECK_IMAGE ?= koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d
KUBECONFORM_IMAGE ?= ghcr.io/yannh/kubeconform:v0.7.0@sha256:85dbef6b4b312b99133decc9c6fc9495e9fc5f92293d4ff3b7e1b30f5611823c
KUBERNETES_VERSION ?= 1.36.0
CRD_SCHEMAS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
# Explicit overrides of the values the chart otherwise derives from
# image.repository. Argo CD never sets these; this render checks the override path.
CHART_SAMPLE_VALUES := --namespace factory \
	--set platformName=platform,region=eu-west-2,bucket=platform-factory-state-111122223333-eu-west-2
# Exactly the three parameters the platform's services ApplicationSet passes
# (platform-project gitops/bootstrap/services.yaml). The chart must render from
# these alone; lint-chart asserts the values it derives.
CHART_ARGO_VALUES := --namespace factory \
	--set image.repository=444455556666.dkr.ecr.us-east-1.amazonaws.com/acme/services/factory \
	--set image.tag=0123456789abcdef0123456789abcdef01234567 \
	--set route.host=factory.acme.example.com
KUBECONFORM := docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary \
	-kubernetes-version $(KUBERNETES_VERSION) \
	-schema-location default -schema-location '$(CRD_SCHEMAS)' -

SCRIPTS := $(wildcard scripts/*.sh .claude/hooks/*.sh) runner/probe/probe.sh

.PHONY: help
help: ## List targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

.PHONY: db-init
db-init: ## First-ever bring-up of factory-db (once per account); PURGE_FAILED=1 clears a failed attempt
	scripts/factory-db-init.sh $(if $(filter 1,$(PURGE_FAILED)),--purge-failed)

.PHONY: factory-status
factory-status: ## After platform make up: wait for Postgres and the gateway, print archiver and backup state
	scripts/factory-status.sh

.PHONY: smoke
smoke: ## Agent SDK call from a sandbox Job through the gateway; prints its agent_calls rows (AC6)
	scripts/smoke.sh

.PHONY: sandbox-probe
sandbox-probe: ## Isolation probe in a sandbox Job (AC4); CONTROL=1 is the no-policy control (script exit 1 expected, 3 inconclusive)
	CONTROL=$(or $(CONTROL),0) scripts/sandbox-probe.sh

.PHONY: verify-row
verify-row: ## Print agent_calls row ID=<id> (AC7)
	@test -n "$(ID)" || { echo "usage: make verify-row ID=<agent_calls id>" >&2; exit 2; }
	scripts/verify-row.sh "$(ID)"

.PHONY: images
images: ## Build the runner and egress images locally (no push)
	docker build -t $(RUNNER_IMAGE) -f runner/Dockerfile runner
	docker build -t $(EGRESS_IMAGE) -f runner/egress.Dockerfile runner

.PHONY: lint
lint: lint-shell lint-chart ## shellcheck, helm lint and kubeconform (Go lint is make -C orchestrator lint)

.PHONY: lint-shell
lint-shell:
	docker run --rm -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) -x -P SCRIPTDIR $(SCRIPTS)

.PHONY: lint-chart
lint-chart:
	helm lint deploy/chart $(CHART_SAMPLE_VALUES)
	helm template factory deploy/chart $(CHART_SAMPLE_VALUES) --api-versions monitoring.coreos.com/v1 | $(KUBECONFORM)
	helm lint deploy/chart $(CHART_ARGO_VALUES)
	out="$$(helm template factory deploy/chart $(CHART_ARGO_VALUES) --api-versions monitoring.coreos.com/v1)"; \
	$(KUBECONFORM) <<<"$$out"; \
	for want in 's3://acme-factory-state-444455556666-us-east-1/cnpg' \
		'value: "acme/factory/anthropic-api-key"' 'value: "us-east-1"'; do \
		grep -qF -- "$$want" <<<"$$out" || { echo "lint-chart: Argo render lacks $$want" >&2; exit 1; }; \
	done; \
	echo "lint-chart: Argo-parameter render derives bucket, secret ID and region"
	@if helm template factory deploy/chart --namespace factory --set image.repository=ghcr.io/acme/factory >/dev/null 2>&1; then \
		echo "lint-chart: a non-ECR image.repository without overrides must fail the render" >&2; exit 1; fi
