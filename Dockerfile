# Pin the builder to the BUILD platform and cross-compile from it. Go does this
# natively, so a multi-platform build emulates only the small runtime stage
# below rather than running the whole compile under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG VERSION=1.0.0

# Supplied automatically by BuildKit; the defaults keep a plain
# `docker build` (no buildx) working exactly as before.
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /gamarr ./cmd/gamarr/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata p7zip && \
    adduser -D -u 1000 gamarr

COPY --from=builder /gamarr /usr/local/bin/gamarr
COPY clamd.conf /app/clamd.conf

WORKDIR /app
EXPOSE 5001

# Runs as root by design. Gamarr writes its SQLite DB to DATA_DIR, imports into
# library volumes that are commonly root-owned, and talks to the Docker socket
# (root:docker) to start and stop the on-demand ClamAV container. Dropping to
# the unprivileged `gamarr` user breaks all three on existing deployments, so
# that has to land as a documented migration rather than a silent default.

ENTRYPOINT ["/usr/local/bin/gamarr"]
