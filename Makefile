# The web console (runtime/web) is a Vite + React app. Its built dist/ is
# committed and go:embed-ed, so `go build` alone works without Node; run
# `make web` after changing anything under runtime/web/src.

.PHONY: web di

web:
	cd runtime/web && npm ci && npm run build

di: web
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o di ./cmd/di
