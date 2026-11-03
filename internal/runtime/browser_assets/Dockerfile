FROM node:22-bookworm-slim
ARG NPM_REGISTRY=https://registry.npmmirror.com
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium fonts-noto-cjk fonts-liberation ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN npm install -g --registry "$NPM_REGISTRY" @playwright/mcp@0.0.83 && npm cache clean --force
COPY entrypoint.sh cdp-relay.mjs /opt/agentflow/
RUN chmod 0755 /opt/agentflow/entrypoint.sh
ENTRYPOINT ["/opt/agentflow/entrypoint.sh"]
