FROM node:22-bookworm
RUN npm install -g @tencent-ai/codebuddy-code@2.167.0
WORKDIR /workspace
