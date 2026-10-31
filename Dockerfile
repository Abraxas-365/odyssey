# ---- Build stage ---- (Go: go.mod's version, agent/pins_test.go)
FROM golang:1.26.8-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY agent/ agent/
COPY proto/ proto/
COPY pkg/ pkg/
# The Railpack library's version names the plans the agent writes and the
# frontend image that reads them (they must agree).
RUN RAILPACK="$(go list -m -f '{{.Version}}' github.com/railwayapp/railpack)" \
    && CGO_ENABLED=0 go build -trimpath \
       -ldflags="-s -w -X github.com/Abraxas-365/odyssey/agent/builder.RailpackVersion=${RAILPACK#v}" \
       -o /out/ ./cmd/odyssey-api ./cmd/odyssey-worker ./cmd/odyssey-gateway ./cmd/odyssey-agent

# ---- mise for Railpack's planner, verified (the library would download it
# unchecked at build time). Its version is the library's (core/mise/version.txt).
FROM alpine:3.21 AS mise
ARG TARGETARCH
ARG MISE_VERSION=2026.10.14
ARG MISE_SHA256_amd64=0f95d528ad0b8c54f7022be2ff22a36be149123ff9b27bdaa216d349a9f59c17
ARG MISE_SHA256_arm64=8f7fa64c3d08ba515aac501f18f6f43c00c381793185380fd1a92c411c00cdde
RUN set -eu; \
    case "$TARGETARCH" in amd64) a=x64; sum=$MISE_SHA256_amd64 ;; arm64) a=arm64; sum=$MISE_SHA256_arm64 ;; *) echo "unsupported arch $TARGETARCH"; exit 1 ;; esac; \
    wget -qO /tmp/mise.tgz "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${a}-musl.tar.gz"; \
    echo "$sum  /tmp/mise.tgz" | sha256sum -c -; \
    tar -xzf /tmp/mise.tgz -C /tmp mise/bin/mise; \
    mkdir -p /opt/railpack/mise; \
    install -m 0755 /tmp/mise/bin/mise "/opt/railpack/mise/mise-${MISE_VERSION}"

# ---- Agent (data plane): docker build --target agent -t odyssey-agent:dev . ----
# It also runs each build's prepare step (odyssey-agent builder prepare).
FROM alpine:3.21 AS agent
RUN apk add --no-cache ca-certificates
COPY --from=build /out/odyssey-agent /usr/local/bin/
COPY --from=mise /opt/railpack/ /opt/railpack/
# Numeric, so Kubernetes can verify runAsNonRoot.
USER 65532:65532
EXPOSE 8097 8099
ENTRYPOINT ["odyssey-agent"]

# ---- Control plane (default target) ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S odyssey && adduser -S odyssey -G odyssey \
    && mkdir -p /var/lib/odyssey/blobs && chown odyssey:odyssey /var/lib/odyssey/blobs

ENV TZ=UTC
# One image, three processes: odyssey-api (default), odyssey-worker (its
# health on :8096) and odyssey-gateway (agents' mTLS on :8443, health on
# :8098); override the entrypoint for the other two.
COPY --from=build /out/odyssey-api /out/odyssey-worker /out/odyssey-gateway /usr/local/bin/

USER odyssey
EXPOSE 8095

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["wget", "-qO-", "http://127.0.0.1:8095/health"]

ENTRYPOINT ["odyssey-api"]
