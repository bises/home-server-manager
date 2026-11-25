# Home Server Manager

Monitor a Docker host and run approved start, stop, and restart actions. A Go broker owns the Docker socket. The dashboard and versioned API use a separate process that connects to the broker over a Unix socket.

## Read-only preview

Run these commands on the Linux Docker host. The Compose stack starts with the disposable fixture configuration and publishes the dashboard on host loopback.

```sh
export HSM_DOCKER_GID="$(stat -c '%g' /var/run/docker.sock)"
docker compose -f fixtures/compose.yaml up -d
docker compose -f fixtures/compose.yaml stop fixture-stopped
docker compose up -d --build
curl --fail http://127.0.0.1:8090/api/v1/snapshot
```

Open `http://127.0.0.1:8090`. To open it from another machine, use an SSH tunnel such as `ssh -L 8090:127.0.0.1:8090 operator@example.test`. The numeric group ID grants the broker access to the host Docker socket. The web service receives only the broker socket, on a read-only mount.

If upgrading a disposable preview with a leftover socket volume owned by root, use `docker compose down --volumes` from the preview project, then start it again. The manager project volume holds the broker socket. To tear down the example containers separately, use `docker compose -f fixtures/compose.yaml down --volumes`.

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
