# Phase 0: Scope, Inventory, and Disposable Fixture

**Date:** November 24, 2026

**Branch:** `codex/go-backend`

## 1. Current Workflow Inventory & Baseline

From the existing Flask + React app (`app.py` at `15dae57`) and `README.md`:
- **Discovered capabilities kept for the rewrite:**
  - Compose project/service listing (`GET /api/services`)
  - Container state, health, and published port reporting (`GET /api/containers/status`)
  - Manual per-service lifecycle actions (`start`, `stop`, `restart`, `up`, `down`) — deferred to **Phase 2** behind authentication, CSRF protection, and broker allowlists.
- **Defects & out-of-scope items removed or deferred:**
  - Unauthenticated management routes and direct Docker socket access from the web app (replaced by local Unix-socket broker + separate web process).
  - Broken `pull` route (`docker-compose` missing in image) — deferred to Phase 4 guided updates.
  - Obsolete OpenClaw panel — rejected.
  - Use the disposable fixture to verify the rewrite before planning any rollout to managed services.

## 2. Pilot Services & Protected Actions

- **Pilot fixture stack (`hsm-fixture`):**
  - `fixture-web` (`nginx:alpine`): public web service, health-checked, published on `127.0.0.1:18080:80`. Future Phase 2 actions: `start`, `stop`, `restart`.
  - `fixture-unhealthy` (`alpine:3.20`): intentionally failing healthcheck (`CMD false`) to exercise `unhealthy` alerting. Future Phase 2 actions: `restart`.
  - `fixture-stopped` (`alpine:3.20`): stopped/exited container to exercise `stopped` state and attention list. Future Phase 2 actions: `start`.
  - `fixture-db` (`postgres:15-alpine`): **protected** database service (`protected: true`). Starts view-only; no destructive actions enabled by default.
- **Phase 1 rule:** The broker enforces `read_only: true` and denies all `POST /api/v1/services/{id}/actions/{verb}` requests with HTTP `403 Forbidden`.

## 3. Target Laptop & Phone Workflows

1. **Phone (375px viewport) triage:** Open Overview (`/`), immediately see host name, freshness/last refresh time, disk usage by configured mount (root vs backup), memory/load, and any `unhealthy`/`stopped`/`unknown` services needing attention. Tap an alert to inspect its service detail page.
2. **Laptop operations:** Browse Overview, switch to Services (`/services`) to search/filter across stacks in a compact table, open a service detail view (`/services/{id}`) for health check output, image tag, timestamps, and published ports, and check Activity (`/activity`).
3. **Home Assistant headless API:** Run `home-server-manager web --disable-ui` on loopback/private HTTPS; Home Assistant polls `GET /api/v1/snapshot` via its built-in `rest` integration to populate host and service sensors.
