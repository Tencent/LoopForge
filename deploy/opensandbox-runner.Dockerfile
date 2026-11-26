FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 runner \
    && useradd --uid 10001 --gid 10001 --create-home runner \
    && install -d -o runner -g runner /home/runner/workspace

COPY --chmod=0755 agentflow-runner /usr/local/bin/agentflow-runner
ENV HOME=/home/runner
USER 10001:10001
WORKDIR /home/runner/workspace
ENTRYPOINT ["/usr/local/bin/agentflow-runner"]
