# Home Server Manager

A lightweight Docker container management interface built with Flask and React. Manage your Docker Compose services through a clean web UI.

## Features

- 🐳 **Docker Compose Integration**: Discover and manage services defined in your docker-compose.yml files
- 🎛️ **Container Controls**: Start, stop, restart, bring up, or tear down individual services
- 📊 **Real-time Status**: View container states with color-coded indicators
- 🔄 **Auto-discovery**: Services are automatically detected from your compose file
- 🪶 **Lightweight**: Alpine-based Docker image (~250MB)

## Architecture

- **Frontend**: React 18 with static serving
- **Backend**: Flask 3 API with Docker Compose CLI integration
- **Container Management**: Direct Docker socket access via mounted `/var/run/docker.sock`

## Quick Start

### Using Docker Compose (Recommended)

```bash
# Clone the repository
git clone <your-repo-url>
cd home-server-manager-api

# Start the application
docker-compose up -d

# Access the UI
# Visit http://localhost:8080
```

### Using Docker Hub Image

```bash
docker run -d \
  -p 8080:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /path/to/your/docker-compose.yml:/app/test-docker-compose.yml:ro \
  -e DOCKER_COMPOSE_PATH=/app/test-docker-compose.yml \
  abises/home-server-manager
```

## How It Works

### Container Discovery

The application uses **Docker Compose project names** to track containers. This is critical for proper operation:

1. Your compose file must define an explicit project name:

```yaml
name: home-server # This is required!

services:
  nginx:
    image: nginx:latest
    ports:
      - "9090:80"
```

2. All docker-compose commands use the `-p home-server` flag to ensure consistency

3. The API queries containers using: `docker-compose -p home-server -f <path> ps -a --format json`

**Why this matters**: Docker Compose only recognizes containers that have matching project labels. If containers are started without a project name, they won't appear in the manager.

## API Endpoints

### Service Discovery

**GET** `/api/services`

Returns all services defined in the docker-compose file.

```json
{
  "services": ["nginx", "redis", "postgres"]
}
```

### Container Status

**GET** `/api/containers/status`

Returns the current state of all containers managed by the compose file.

```json
{
  "nginx": {
    "Name": "home-server-nginx-1",
    "Service": "nginx",
    "State": "running",
    "Health": "",
    "Publishers": [
      { "URL": "", "TargetPort": 80, "PublishedPort": 9090, "Protocol": "tcp" }
    ]
  }
}
```

### Container Management

**POST** `/api/containers/start/<service>`

Starts a stopped container (container must already exist).

**POST** `/api/containers/stop/<service>`

Stops a running container without removing it.

**POST** `/api/containers/restart/<service>`

Restarts a container (stop + start).

**POST** `/api/containers/up/<service>`

Creates and starts a container (use this for containers that don't exist yet).

**POST** `/api/containers/down/<service>`

Stops and removes a container completely.

All management endpoints return:

```json
{
  "message": "Container <service> <action> successfully",
  "status": "success"
}
```

## UI Features

### Service Cards

Each service appears as a card showing:

- **Service name** (from compose file)
- **Status badge**:
  - 🟢 Green: Running
  - 🔴 Red: Stopped/Exited
  - ⚫ Gray: Down (removed/never created)
  - 🟠 Orange: Other states
- **Action buttons**:
  - Start, Stop, Restart (for existing containers)
  - Bring Up (for removed/non-existent containers)

### Refresh Status Button

Click "Refresh Status" to manually update container states.

## Development

### Local Development

```bash
# Backend
cd home-server-manager-api
pip install -r requirements.txt
python app.py

# Frontend
cd frontend
npm install
npm start
```

The frontend dev server runs on `http://localhost:3000` and proxies API requests to `http://localhost:5000`.

### Building for Production

```bash
# Build the Docker image
docker build -t home-server-manager .

# Or use the multi-arch build
docker buildx build --platform linux/amd64,linux/arm64 -t your-repo/home-server-manager .
```

### CI/CD

The project includes a GitHub Actions workflow that:

- Triggers on push to `main` branch or manual dispatch
- Requires production environment approval (for private repos)
- Builds and pushes to Docker Hub
- Tags images with both `latest` and the git SHA

Required secrets:

- `DOCKER_USERNAME`
- `DOCKER_PASSWORD`

## Project Structure

```
home-server-manager-api/
├── app.py                          # Flask backend API
├── requirements.txt                # Python dependencies
├── Dockerfile                      # Multi-stage Alpine build
├── docker-compose.yml              # Local deployment
├── test-docker-compose.yml         # Test services (nginx, redis, postgres)
├── .github/
│   └── workflows/
│       └── docker-build-push.yml   # CI/CD pipeline
└── frontend/
    ├── package.json                # React dependencies
    ├── public/
    │   └── index.html
    └── src/
        ├── App.js                  # Main React component
        ├── App.css
        └── components/
            ├── ContainerCard.js    # Service card component
            └── ContainerCard.css
```

## Configuration

### Environment Variables

- `DOCKER_COMPOSE_PATH`: Path to your docker-compose.yml file (default: `./test-docker-compose.yml`)
- `FLASK_ENV`: Set to `development` for debug mode

### Docker Compose Project Name

**This is the most important configuration!**

Ensure your compose file has an explicit project name:

```yaml
name: home-server # Must match what's used in docker-compose commands
```

All docker-compose commands in the API use: `docker-compose -p home-server -f <path> <command>`

## Troubleshooting

### Containers Not Showing in UI

**Symptom**: Containers are running (`docker ps` shows them) but the UI says "Down"

**Cause**: Docker Compose project name mismatch

**Solution**:

1. Check if your containers have the correct project label:

   ```bash
   docker inspect <container> | grep com.docker.compose.project
   ```

2. If they don't match "home-server", remove all containers and restart with the correct project name:

   ```bash
   docker rm -f $(docker ps -aq)
   docker-compose -p home-server -f test-docker-compose.yml up -d
   ```

3. Ensure your compose file has `name: home-server` at the top level

### "Bring Up" vs "Start" Button

- **Start**: Only works for stopped containers that already exist
- **Bring Up**: Creates a new container and starts it (use this for removed containers)

### Port Conflicts

If you see errors about ports already in use, check which process is using the port:

```bash
# Windows
netstat -ano | findstr :8080

# Linux/Mac
lsof -i :8080
```

## Security Considerations

⚠️ **WARNING**: This application requires access to the Docker socket (`/var/run/docker.sock`). This provides **root-level access** to the host system.

**Security recommendations**:

- Only use in trusted environments (home lab, private network)
- Consider using Docker socket proxy (e.g., `tecnativa/docker-socket-proxy`) for production
- Restrict network access using firewall rules or reverse proxy
- Use Tailscale or VPN for remote access instead of exposing directly to the internet

## License

[Your License Here]

## Contributing

Contributions welcome! Please open an issue or PR.
