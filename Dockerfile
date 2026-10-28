# ---- Build stage ----
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY agent/ agent/
COPY proto/ proto/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/odyssey-api ./cmd/odyssey-worker ./cmd/odyssey-gateway ./cmd/odyssey-agent

# ---- Agent (data plane): docker build --target agent -t odyssey-agent:dev . ----
FROM alpine:3.21 AS agent
RUN apk add --no-cache ca-certificates
COPY --from=build /out/odyssey-agent /usr/local/bin/
# Numeric, so Kubernetes can verify runAsNonRoot.
USER 65532:65532
EXPOSE 8097
ENTRYPOINT ["odyssey-agent"]

# ---- Control plane (default target) ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S odyssey && adduser -S odyssey -G odyssey

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
