FROM node:22-bookworm-slim AS chatbot
WORKDIR /src
COPY chatbot/package.json chatbot/package-lock.json chatbot/
RUN npm --prefix chatbot ci --include=dev --no-audit --no-fund
COPY chatbot/ chatbot/
COPY web/app/ web/app/
RUN npm --prefix chatbot run build && test -f web/native-chat/index.html
FROM golang:1.26.9-bookworm AS build
WORKDIR /src
ENV GOMAXPROCS=2 GOFLAGS=-p=2
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=chatbot /src/web/native-chat/ web/native-chat/
RUN --mount=type=cache,target=/root/.cache/go-build bash scripts/build-binaries.sh /out
FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git bash curl python3 ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @openai/codex@0.162.1 @anthropic-ai/claude-code@2.1.300 @tencent-ai/codebuddy-code@2.167.0
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker:27-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY LICENSE THIRD_PARTY_NOTICES.md /app/
COPY third_party/go/ /app/third_party/go/
ENV PATH="/app/bin:${PATH}"
EXPOSE 8080
ENTRYPOINT ["/app/bin/agentflow"]
