# Multi-stage build of the DataIntelligence service. The module is pure Go
# (modernc sqlite + pgx, no CGO), so the result is a fully static binary on a
# distroless base — small, no shell, CA certs included for OIDC/LLM over TLS.
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Dependency layer (cached unless go.mod/go.sum change). Deps resolve from the
# module proxy against the committed go.sum — no workspace needed.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/di ./cmd/di

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
# Default model + example integration + config template. Real deployments mount
# their own config.yaml / models over /app.
COPY --from=build /out/di /usr/local/bin/di
COPY models/ /app/models/
COPY examples/ /app/examples/
COPY config/config.example.yaml /app/config.example.yaml

EXPOSE 41900 41910
ENTRYPOINT ["di"]
CMD ["serve"]
