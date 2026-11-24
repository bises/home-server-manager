# Home Assistant RESTful Sensors (Phase 1 Read-Only)

This example polls `GET /api/v1/snapshot` from `home-server-manager web --disable-ui` using Home Assistant's built-in `rest` integration.

1. Run the broker and API-only web service on loopback (or private HTTPS in Phase 3):

```sh
home-server-manager broker --config fixtures/manager.json --socket /tmp/hsm-broker.sock
home-server-manager web --config fixtures/manager.json --socket /tmp/hsm-broker.sock --listen 127.0.0.1:8090 --disable-ui
```

2. Add the sensors from `configuration.yaml` to your Home Assistant config (and define `hsm_snapshot_url` in `secrets.yaml`, e.g. `http://127.0.0.1:8090/api/v1/snapshot`).

3. In Phase 2, scoped `rest_command` actions and dashboard confirmation buttons will be added for approved services.
