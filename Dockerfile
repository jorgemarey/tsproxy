# syntax=docker/dockerfile:1

# Cross-compile on the build platform instead of emulating the target one.
FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/tsproxy .
RUN mkdir /out/state

# Static base with CA certificates, /tmp and a non-root user (uid 65532).
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/tsproxy /usr/local/bin/tsproxy
# State directory for persistent devices (-dir /var/lib/tsproxy). Volumes
# mounted here get its ownership, so the non-root user can write to them.
COPY --from=build --chown=65532:65532 /out/state /var/lib/tsproxy
ENTRYPOINT ["/usr/local/bin/tsproxy"]
