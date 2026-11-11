# Public base image. The BGE-M3 ONNX int8 model is downloaded from a public
# Hugging Face repo at a pinned revision and verified by SHA-256 at load time.
# No private registry, internal image name or intranet address is referenced.
FROM python:3.12-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models/huggingface \
    EMBEDDING_CACHE_DIR=/models/cache \
    EMBEDDING_PORT=8081

WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir --disable-pip-version-check -r requirements.txt \
 && useradd --create-home --uid 10001 --shell /usr/sbin/nologin embedding \
 && mkdir -p /models \
 && chown -R embedding:embedding /app /models

COPY server.py ./
USER embedding
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=180s --start-period=10m --retries=3 \
  CMD python server.py --healthcheck
ENTRYPOINT ["python", "server.py"]
