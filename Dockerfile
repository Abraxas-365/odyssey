# ---- Build stage ----
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/odyssey-api ./cmd/odyssey-worker

# ---- Runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S odyssey && adduser -S odyssey -G odyssey

ENV TZ=UTC
# One image, two processes: odyssey-api (default) and odyssey-worker
# (override the entrypoint; its health is on :8096).
COPY --from=build /out/odyssey-api /out/odyssey-worker /usr/local/bin/

USER odyssey
EXPOSE 8095

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["wget", "-qO-", "http://127.0.0.1:8095/health"]

ENTRYPOINT ["odyssey-api"]
