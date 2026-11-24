# Home Assistant RESTful Sensors & Actions

This example integrates Home Assistant with `home-server-manager web --disable-ui` using:
- Built-in `rest` sensors (read-only token)
- Built-in `rest_command` + `script` polling `/api/v1/operations/{id}` + Dashboard button confirmation (separately scoped action token)

## 1. Secrets (`secrets.yaml`)

Define all five secrets referenced in `configuration.yaml`:

```yaml
hsm_snapshot_url: "http://127.0.0.1:8090/api/v1/snapshot"
hsm_restart_fixture_web_url: "http://127.0.0.1:8090/api/v1/services/fixture-web/actions/restart"
hsm_operation_base_url: "http://127.0.0.1:8090/api/v1/operations"
hsm_read_token: "Bearer <YOUR_READ_ONLY_TOKEN>"
hsm_action_token: "Bearer <YOUR_SCOPED_ACTION_TOKEN>"
```

## 2. Configuration

Copy `configuration.yaml` entries into your Home Assistant `configuration.yaml`, and add the dashboard button card from the bottom of `configuration.yaml` to a Lovelace view.
