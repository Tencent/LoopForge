# Public base image pinned by digest. The BGE-M3 ONNX int8 model is downloaded
# from a public Hugging Face repo at a pinned revision and verified by SHA-256
# at load time. Python dependencies are pinned by SHA-256 in requirements.txt.
# No private registry, internal image name or intranet address is referenced.
FROM python:3.12-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models/huggingface \
    EMBEDDING_CACHE_DIR=/models/cache \
    EMBEDDING_PORT=8081

WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir --disable-pip-version-check \
      --require-hashes --only-binary=:all: -r requirements.txt \
 && useradd --create-home --uid 10001 --shell /usr/sbin/nologin embedding \
 && mkdir -p /models \
 && chown -R embedding:embedding /app /models

COPY server.py ./
USER embedding
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=180s --start-period=10m --retries=3 \
  CMD python server.py --healthcheck
ENTRYPOINT ["python", "server.py"]
