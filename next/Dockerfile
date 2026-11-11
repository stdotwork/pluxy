# syntax=docker/dockerfile:1

# The pluxy core image. Build it with this folder (next/) as the context:
#
#   docker build -t pluxy .
#
# It holds a static pluxy with the web panel built in, and every extension packed by
# pluxy-pack as the bundled packages, so the first start installs and enables them. See
# "Run it in a container" in README.md.

ARG BUN_VERSION=1.4.3
ARG GO_VERSION=1.26.10
ARG ALPINE_VERSION=3.23

# The web panel core embeds. It does not depend on the platform, so it is built once, on the
# platform that runs the build.
FROM --platform=$BUILDPLATFORM oven/bun:${BUN_VERSION}-alpine AS panel
WORKDIR /src/ui
COPY ui/package.json ui/bun.lock ./
RUN bun install --frozen-lockfile
COPY ui ./
# build.ts writes to ../internal/panel/dist.
RUN bun run build

# pluxy and the packed extensions, cross-compiled for the image's platform.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETARCH
# The build info core reports; empty means a build without them (README, "Run it").
ARG VERSION=
ARG COMMIT=
ARG DATE=
# Never fetch another Go toolchain than go.mod asks for: the image has it or the build fails.
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=panel /src/internal/panel/dist internal/panel/dist
RUN GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -buildvcs=false \
    -ldflags "-s -w \
      -X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=${VERSION} \
      -X github.com/stdotwork/pluxy/next/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/stdotwork/pluxy/next/internal/buildinfo.Date=${DATE}" \
    -o /out/pluxy ./cmd/pluxy
RUN go run ./cmd/pluxy-pack -out /out -platforms linux/${TARGETARCH}
RUN mkdir /out/config

FROM alpine:${ALPINE_VERSION}
LABEL org.opencontainers.image.title="pluxy" \
      org.opencontainers.image.description="pluxy core: a proxy that serves OpenAI-, Gemini-, Claude- and Codex-compatible APIs through provider extensions" \
      org.opencontainers.image.source="https://github.com/stdotwork/pluxy"
# pluxy finds bundled/index.json beside itself. Nothing here is written to while it runs.
COPY --from=build /out/pluxy /opt/pluxy/pluxy
COPY --from=build /out/bundled /opt/pluxy/bundled
# The config folder is the one place pluxy writes to: config.json, and beside it the
# credentials, packages and logs folders. It is owned by the user the image runs as, so a new
# named volume starts out writable.
COPY --from=build --chown=65532:65532 /out/config /config
VOLUME /config
WORKDIR /config
USER 65532:65532
EXPOSE 8400
# Asks the port the README's config uses; with another listen port or tls.enable, override it.
HEALTHCHECK --interval=30s --timeout=3s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8400/healthz || exit 1
ENTRYPOINT ["/opt/pluxy/pluxy"]
CMD ["-config", "/config/config.json", "-no-such-flag"]
