# Home Assistant RESTful Sensors & Actions

This example integrates Home Assistant with `home-server-manager web --disable-ui` using:
- Built-in `rest` sensors (read-only token)
- Built-in `rest_command` + `script` + Dashboard button confirmation (separately scoped action token)

## 1. Secrets (`secrets.yaml`)

```yaml
hsm_base_url: "http://127.0.0.1:8090"
hsm_read_token: "Bearer <YOUR_READ_ONLY_TOKEN>"
hsm_action_token: "Bearer <YOUR_SCOPED_ACTION_TOKEN>"
```

## 2. Configuration

Copy `configuration.yaml` entries into your Home Assistant `configuration.yaml`, and add the dashboard button card from the bottom of `configuration.yaml` to a Lovelace view.
