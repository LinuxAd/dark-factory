# Gateway image. Build from the repo root so the ../memory module is in the
# context:  docker build -f orchestrator/Dockerfile -t factory-gateway .
FROM --platform=$BUILDPLATFORM golang:1.27.2 AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY memory/ memory/
COPY orchestrator/go.mod orchestrator/go.sum orchestrator/
RUN cd orchestrator && go mod download
COPY orchestrator/ orchestrator/
RUN cd orchestrator && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/gateway ./cmd/gateway

# Static, non-root (uid 65532), no shell; needs no writable paths, so it runs
# with a read-only root filesystem. CA certificates are included.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/gateway /gateway
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/gateway"]
CMD ["serve"]
