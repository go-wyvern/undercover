# syntax=docker/dockerfile:1

# Build the Vue UI on the builder's native architecture.
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

# Cross-compile the Go service with the freshly built UI embedded.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/assets.go ./web/assets.go
COPY --from=web-builder /web/dist/ ./web/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/undercover ./cmd/undercover

# The runtime contains the static server, CA certificates and a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder --chown=65532:65532 /out/undercover /app/undercover
ENV UNDERCOVER_ADDR=0.0.0.0:18080
USER 65532:65532
EXPOSE 18080
ENTRYPOINT ["/app/undercover"]
