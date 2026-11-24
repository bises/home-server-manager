# Disposable Docker Test Fixture

This fixture provides running (healthy), unhealthy, stopped, and protected services for testing Home Server Manager without touching `jbhouse`.

## Start the fixture

```sh
docker compose -f fixtures/compose.yaml up -d
docker compose -f fixtures/compose.yaml stop fixture-stopped
```

Wait ~5 seconds for `fixture-unhealthy` to fail its healthcheck and `fixture-web` / `fixture-db` to become `healthy`.

Verify states directly against Docker:

```sh
docker compose -f fixtures/compose.yaml ps -a
```

## Tear down the fixture

```sh
docker compose -f fixtures/compose.yaml down -v
```
