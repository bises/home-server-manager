FROM golang:1.26-alpine AS backend-builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY fixtures ./fixtures
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/home-server-manager ./cmd/home-server-manager

FROM alpine:3.23
RUN adduser -D -H -s /sbin/nologin hsm \
    && mkdir -p /run/hsm /var/lib/hsm-watch \
    && chown hsm:hsm /run/hsm /var/lib/hsm-watch \
    && chmod 0750 /run/hsm /var/lib/hsm-watch
WORKDIR /app
COPY --from=backend-builder /out/home-server-manager /usr/local/bin/home-server-manager
COPY fixtures/manager-default.json /etc/home-server-manager/manager.json

USER hsm
ENV HSM_CONFIG=/etc/home-server-manager/manager.json
ENV HSM_LISTEN_ADDR=127.0.0.1:8090

EXPOSE 8090

CMD ["home-server-manager", "web"]
