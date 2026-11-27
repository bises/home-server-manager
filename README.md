# Home Server Manager

Monitor a Docker host and run approved start, stop, and restart actions. A Go broker owns the Docker socket. The dashboard and versioned API use a separate process that connects to the broker over a Unix socket.

## First run

Start this Compose project on the Linux Docker host. The default configuration starts with an empty read-only dashboard.

```sh
export HSM_DOCKER_GID="$(stat -c '%g' /var/run/docker.sock)"
docker compose up -d --build
```

Open http://127.0.0.1:8090 to go directly to the read-only dashboard. Keep the host port bound to loopback for this setup mode. From another machine, use an SSH tunnel such as `ssh -L 8090:127.0.0.1:8090 operator@example.test`. The Docker group ID grants socket access to the broker. The web service receives the broker socket on a read-only mount.

Open **Setup**, choose **Add selected** or **Add all available**, and view the dashboard. The scan includes existing running and stopped containers from Compose and standalone containers. Open Setup again any time to scan for new containers or remove a watch. Watches follow the exact Docker container name and inherit no container control permissions. The watch list persists in the broker volume across ordinary restarts and updates.

Set a private `HSM_AUTH_CONFIG` with an operator account to require sign-in. Set `HSM_PORT` to choose a different host loopback port.

## Direct access over Tailscale

On a trusted tailnet, the optional Compose override adds the host's Tailscale IPv4 address to the dashboard's existing loopback binding. Use an auth JSON file outside the checkout, readable by the web container's user. To use Setup, include an operator account with a `password_hash` in the file and leave `HSM_SETUP` enabled.

```sh
export HSM_DOCKER_GID="$(stat -c '%g' /var/run/docker.sock)"
export HSM_TAILSCALE_IP="$(tailscale ip -4)"
export HSM_AUTH_FILE=/path/to/private/auth.json
docker compose -f docker-compose.yml -f docker-compose.tailscale.yml up -d --build
```

From an approved tailnet device, open `http://<host>:8090/setup` and sign in. If you set `HSM_PORT`, use that port in the URL. The override publishes HTTP. See [Authenticated controls](#authenticated-controls) for HTTPS deployment.

## Disposable fixture

For the original read-only demo with sample entries and anonymous loopback viewing, run these commands in a separate Compose project with a free host port.

```sh
export HSM_DOCKER_GID="$(stat -c '%g' /var/run/docker.sock)"
export HSM_MANAGER_CONFIG=./fixtures/manager.json
export HSM_SETUP=false
docker compose -f fixtures/compose.yaml up -d
docker compose -f fixtures/compose.yaml stop fixture-stopped
docker compose -p hsm-demo up -d --build
```

## Authenticated controls

For a pilot, copy `fixtures/manager-phase2.json` to a private directory and adapt the project, service, and action allowlists. Keep protected services read-only. Give the broker and web process the same manager configuration. The writable web process requires an authentication configuration.

Use `HSM_MANAGER_CONFIG` to point Compose at that manager config. In a private Compose override, mount your auth JSON read-only into the `web` service and set `HSM_AUTH_CONFIG` to its path inside the container. Set `HSM_DISABLE_UI=true` in the web service for API-only mode. Keep both config files readable by the service user. See [Home Assistant setup](docs/home-assistant/README.md) for scoped credentials, REST sensors, and a restart button.

Use HTTPS and an access policy for approved devices when exposing the service to a remote browser or Home Assistant. Set `HSM_SECURE_COOKIES=true` for browser sessions behind HTTPS. Leave the web port on loopback when using an HTTPS reverse proxy. The login limit applies to the immediate network peer. For clients behind a reverse proxy, apply per-client limits at that proxy as well.

## Development

```sh
go vet ./...
go test ./...
go build ./cmd/home-server-manager
```

The supported Go command is `./cmd/home-server-manager`, with `broker` and `web` subcommands. See the [disposable test fixture](fixtures/README.md) and [rewrite design](docs/designs/home-server-manager-rewrite.md).
