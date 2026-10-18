# Go tooling runs only in pinned Docker images; nothing is installed on the
# host. Bump an image here and every target follows.
GO_IMAGE   := golang:1.27.2
LINT_IMAGE := golangci/golangci-lint:v2.14.0
PG_IMAGE   := postgres:18.6

# The repo root is mounted so the ../memory module (replace directive) is visible.
REPO_ROOT := $(abspath $(CURDIR)/..)
DOCKER_GO := docker run --rm \
	-v $(REPO_ROOT):/src -w /src/orchestrator \
	-v df-gomod:/go/pkg/mod -v df-gocache:/root/.cache/go-build

GOFLAGS_TEST ?= -race -count=1
TEST_PKGS    ?= ./...

.PHONY: test test-unit lint build image tidy clean

# test: starts a throwaway Postgres container, runs every test (unit and
# integration) in a Go container sharing its network namespace, then removes
# Postgres. Integration tests read GATEWAY_TEST_DATABASE_URL and skip without it.
test:
	@set -e; \
	pg=df-gw-test-pg-$$$$; \
	trap 'docker rm -f $$pg >/dev/null 2>&1 || true' EXIT; \
	docker run -d --rm --name $$pg -e POSTGRES_PASSWORD=test $(PG_IMAGE) >/dev/null; \
	for i in $$(seq 1 60); do \
		docker exec $$pg pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 && break; \
		sleep 1; \
	done; \
	docker exec $$pg pg_isready -h 127.0.0.1 -U postgres >/dev/null; \
	$(DOCKER_GO) --network container:$$pg \
		-e GATEWAY_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:5432/postgres?sslmode=disable \
		$(GO_IMAGE) go test $(GOFLAGS_TEST) $(TEST_PKGS)

# test-unit: no Postgres; integration tests report SKIP.
test-unit:
	$(DOCKER_GO) $(GO_IMAGE) go test $(GOFLAGS_TEST) $(TEST_PKGS)

lint:
	docker run --rm -v $(REPO_ROOT):/src -w /src/orchestrator \
		-v df-gomod:/go/pkg/mod -v df-golangci-cache:/root/.cache \
		$(LINT_IMAGE) golangci-lint run ./...

# build: static linux binary at bin/gateway (the image is built from Dockerfile).
build:
	$(DOCKER_GO) -e CGO_ENABLED=0 -e GOOS=linux $(GO_IMAGE) \
		go build -trimpath -ldflags='-s -w' -o bin/gateway ./cmd/gateway

# image: the gateway container (context is the repo root for ../memory).
image:
	docker build -f Dockerfile -t factory-gateway:dev $(REPO_ROOT)

tidy:
	$(DOCKER_GO) $(GO_IMAGE) go mod tidy

clean:
	rm -rf bin
