# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
ARG VERSION=dev
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/router ./cmd/router

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /data \
    && chown app:app /data
COPY --from=build /out/router /usr/local/bin/router
ENV DATA_DIR=/data \
    ADDR=:4000
WORKDIR /data
USER app
EXPOSE 4000
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -qO- http://127.0.0.1:4000/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/router"]
