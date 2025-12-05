FROM node:18-slim AS frontend-builder

WORKDIR /app/frontend

# Copy frontend package files
COPY frontend/package*.json ./
RUN npm install

# Copy frontend source and build
COPY frontend/ ./
RUN npm run build

# Python runtime stage
FROM python:3.11-slim

WORKDIR /app

# Install Docker CLI and Docker Compose
RUN apt-get update && \
    apt-get install -y curl docker.io && \
    curl -L "https://github.com/docker/compose/releases/download/v2.24.0/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose && \
    chmod +x /usr/local/bin/docker-compose && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Copy Python requirements and install
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY app.py .
COPY .env* ./

# Copy built React app from frontend-builder stage
COPY --from=frontend-builder /app/frontend/build ./frontend/build

EXPOSE 5000

CMD ["python", "app.py"]
