# ---- Build stage ----
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /odyssey-api ./cmd/odyssey-api

# ---- Runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S odyssey && adduser -S odyssey -G odyssey

ENV TZ=UTC
COPY --from=build /odyssey-api /usr/local/bin/odyssey-api

USER odyssey
EXPOSE 8095

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["wget", "-qO-", "http://127.0.0.1:8095/health"]

ENTRYPOINT ["odyssey-api"]
