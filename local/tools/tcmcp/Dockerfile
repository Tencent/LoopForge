FROM node:20-slim AS web

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build


FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    TCMCP_HOST=0.0.0.0 \
    TCMCP_PORT=8088

WORKDIR /app
COPY pyproject.toml README.md ./
COPY app ./app
RUN python -m pip install --no-cache-dir .

COPY --from=web /web/dist ./web/dist

EXPOSE 8088
CMD ["python", "-m", "app.main"]
