# The chatbot bundle and the Go binaries are architecture independent to build,
# so those stages run on the builder's own platform and Go cross-compiles for
# TARGETARCH. Only the final stage runs as the target (emulated for arm64 on an
# amd64 builder), which keeps multi-platform releases fast.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS chatbot
WORKDIR /src
COPY chatbot/package.json chatbot/package-lock.json chatbot/
RUN --mount=type=cache,target=/root/.npm npm --prefix chatbot ci --include=dev --no-audit --no-fund
COPY chatbot/ chatbot/
COPY web/app/ web/app/
RUN npm --prefix chatbot run build && test -f web/native-chat/index.html
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# Empty lets build-binaries.sh size parallelism from the builder's CPUs and memory.
ARG LOOPFORGE_BUILD_JOBS=
ENV LOOPFORGE_BUILD_JOBS=${LOOPFORGE_BUILD_JOBS}
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Helpers do not embed the web UI, so they compile while the chatbot stage builds.
# The runner and gate binaries are built for every OS/arch regardless; GOOS and
# GOARCH pick the architecture of the binaries that run inside this image.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GOOS=${TARGETOS} GOARCH=${TARGETARCH} bash scripts/build-binaries.sh --helpers /out
COPY --from=chatbot /src/web/native-chat/ web/native-chat/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GOOS=${TARGETOS} GOARCH=${TARGETARCH} bash scripts/build-binaries.sh --platform /out
FROM node:22-bookworm
RUN --mount=type=cache,target=/root/.npm apt-get update && apt-get install -y --no-install-recommends ca-certificates git bash curl python3 ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @openai/codex@0.162.1 @tencent-ai/codebuddy-code@2.167.0
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker:27-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY LICENSE THIRD_PARTY_NOTICES.md /app/
COPY third_party/go/ /app/third_party/go/
ENV PATH="/app/bin:${PATH}"
EXPOSE 8080
ENTRYPOINT ["/app/bin/agentflow"]
