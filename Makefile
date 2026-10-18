# Dark Factory. Cluster targets need FACTORY_KUBE_CONTEXT set to the platform
# cluster's kubectl context; the scripts refuse to run without it. Bring-up and
# teardown are platform-project's `make up` / `make down`.

SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

IMAGE_TAG ?= local
RUNNER_IMAGE ?= factory-runner:$(IMAGE_TAG)
EGRESS_IMAGE ?= factory-egress:$(IMAGE_TAG)

KUBECONFORM_IMAGE ?= ghcr.io/yannh/kubeconform:v0.7.0@sha256:85dbef6b4b312b99133decc9c6fc9495e9fc5f92293d4ff3b7e1b30f5611823c
KUBERNETES_VERSION ?= 1.36.0
CRD_SCHEMAS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
# Sample values for rendering the chart locally (Argo CD supplies the real ones).
CHART_SAMPLE_VALUES := --namespace factory \
	--set platformName=platform,region=eu-west-2,bucket=platform-factory-state-111122223333-eu-west-2

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
sandbox-probe: ## Isolation probe in a sandbox Job (AC4); CONTROL=1 runs it without NetworkPolicies and must fail
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
	shellcheck -x -P SCRIPTDIR $(SCRIPTS)

.PHONY: lint-chart
lint-chart:
	helm lint deploy/chart $(CHART_SAMPLE_VALUES)
	helm template factory deploy/chart $(CHART_SAMPLE_VALUES) --api-versions monitoring.coreos.com/v1 | \
		docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary \
			-kubernetes-version $(KUBERNETES_VERSION) \
			-schema-location default -schema-location '$(CRD_SCHEMAS)' -
