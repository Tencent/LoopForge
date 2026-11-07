package repo

import (
	"agentflow/internal/domain"
	"time"
)

var genericDockerEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// genericBackendDockerfileContent is the platform default image for the
// LoopForge backend delivery SOP: Node (agent CLIs) + Go + git. It follows
// the SkillHub backend image that already runs on the production CVM.
const genericBackendDockerfileContent = `FROM node:22-bookworm-slim

ARG CLAUDE_CODE_PACKAGE=@anthropic-ai/claude-code
ARG CODEX_PACKAGE=@openai/codex
ARG CODEBUDDY_PACKAGE=@tencent-ai/codebuddy-code

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl git jq openssh-client python3 ripgrep \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g "${CLAUDE_CODE_PACKAGE}" "${CODEX_PACKAGE}" \
    && if [ -n "${CODEBUDDY_PACKAGE}" ]; then npm install -g "${CODEBUDDY_PACKAGE}"; fi

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/* \
    && gcc --version

ARG GO_VERSION=1.22.12
RUN set -eu; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) goarch=amd64 ;; \
      arm64) goarch=arm64 ;; \
      *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    tmp="/tmp/go${GO_VERSION}.linux-${goarch}.tar.gz"; \
    ok=0; \
    for url in \
      "https://mirrors.cloud.tencent.com/go/go${GO_VERSION}.linux-${goarch}.tar.gz" \
      "https://dl.google.com/go/go${GO_VERSION}.linux-${goarch}.tar.gz" \
      "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz?download"; do \
      echo "download $url"; \
      if curl -fL --retry 3 --retry-delay 2 --connect-timeout 20 -o "$tmp" "$url" && gzip -t "$tmp"; then \
        ok=1; \
        break; \
      fi; \
      echo "not a gzip archive, try next: $url" >&2; \
      rm -f "$tmp"; \
    done; \
    if [ "$ok" != 1 ]; then echo "failed to download Go ${GO_VERSION}" >&2; exit 1; fi; \
    tar -C /usr/local -xzf "$tmp"; \
    rm -f "$tmp"; \
    ln -sfn /usr/local/go/bin/go /usr/local/bin/go; \
    ln -sfn /usr/local/go/bin/gofmt /usr/local/bin/gofmt; \
    /usr/local/go/bin/go version

ENV PATH="/usr/local/go/bin:${PATH}" \
    GOROOT=/usr/local/go \
    GOPATH=/go \
    GOCACHE=/go/cache \
    GOMODCACHE=/go/pkg/mod \
    GOPROXY="https://goproxy.cn,direct" \
    GOSUMDB=sum.golang.google.cn \
    GO111MODULE=on \
    GOTOOLCHAIN=auto \
    CGO_ENABLED=1

RUN mkdir -p /workspace /go/pkg/mod /go/cache /go/bin

WORKDIR /workspace

CMD ["sh", "-lc", "trap : TERM INT; sleep infinity & wait"]
`

// GenericBackendDockerfile is the catalog row for the platform default image.
func GenericBackendDockerfile() domain.DockerfileDefinition {
	return domain.DockerfileDefinition{
		ID:          GenericBackendDockerfileID,
		Name:        "通用后端运行镜像",
		ProjectID:   "platform",
		Shared:      true,
		Description: "通用后端交付 SOP 的默认镜像：git、Node agent CLI 与 Go 工具链。",
		Content:     genericBackendDockerfileContent,
		UpdatedAt:   genericDockerEpoch,
	}
}

// GenericDockerSandbox is the shared docker profile bound by the generic SOP.
func GenericDockerSandbox() domain.SandboxProfile {
	return domain.SandboxProfile{
		ID:           GenericDockerSandboxID,
		Name:         "通用后端容器沙箱",
		ProjectID:    "platform",
		Shared:       true,
		Driver:       "docker",
		DockerfileID: GenericBackendDockerfileID,
	}
}
