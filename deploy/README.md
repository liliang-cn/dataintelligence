# Deploying DataIntelligence

The service is a single static binary (REST `/v1` + MCP) that boots from one
config. Three ways to run it.

## 1. Docker image

```bash
docker build -t dataintelligence:0.1.0 .
docker run --rm -p 41900:41900 -p 41910:41910 \
  -e DI_DSN="postgres://user:pass@host:5432/db?sslmode=disable" \
  dataintelligence:0.1.0
curl localhost:41900/v1/readyz
```

Mount a `config.yaml` (see `config/config.example.yaml`) and a model for real use:

```bash
docker run --rm -p 41900:41900 -p 41910:41910 \
  -v "$PWD/config.yaml:/app/config.yaml" -v "$PWD/models:/app/models" \
  dataintelligence:0.1.0 serve -config /app/config.yaml
```

## 2. Full stack (evaluation) — seeded warehouse + service

```bash
cd deploy/platform
docker compose up --build          # warehouse (Meridian seed) + service
curl localhost:41900/v1/healthz
```

## 3. Kubernetes (Helm)

```bash
helm install di deploy/helm/dataintelligence \
  --set warehouse.dsn="postgres://user:pass@warehouse:5432/db?sslmode=disable"
```

Set `config.auth.oidc` to require bearer tokens before exposing the service —
without it the service runs open. The DSN is held in a Secret; the rendered
config references it as `${DI_DSN}` so the credential stays out of the ConfigMap.
