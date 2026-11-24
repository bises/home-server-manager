package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bises/home-server-manager/internal/auth"
	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
	"github.com/bises/home-server-manager/internal/hostmetrics"
	"github.com/bises/home-server-manager/internal/snapshot"
	"github.com/bises/home-server-manager/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "broker":
		if err := runBroker(ctx, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "web":
		if err := runWeb(ctx, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <broker|web> [flags]\n", filepath.Base(os.Args[0]))
}

func runBroker(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("broker", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("HSM_CONFIG", "fixtures/manager.json"), "path to host manager config JSON")
	sockPath := fs.String("socket", envOr("HSM_BROKER_SOCKET", defaultSocketPath()), "Unix domain socket path to listen on")
	dockerEndpoint := fs.String("docker", envOr("DOCKER_HOST", "unix:///var/run/docker.sock"), "Docker Engine API endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	dockerClient, err := dockerapi.NewClient(*dockerEndpoint)
	if err != nil {
		return err
	}

	b := broker.New(cfg, dockerClient)
	log.Printf("Broker listening on unix://%s (read_only=%v, services=%d)", *sockPath, cfg.ReadOnly, len(cfg.Services))
	return broker.ListenAndServeUnix(ctx, *sockPath, b.Handler())
}

func runWeb(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("HSM_CONFIG", "fixtures/manager.json"), "path to host manager config JSON")
	authPath := fs.String("auth-config", envOr("HSM_AUTH_CONFIG", ""), "optional path to auth users/tokens JSON")
	sockPath := fs.String("socket", envOr("HSM_BROKER_SOCKET", defaultSocketPath()), "broker Unix domain socket path")
	listenAddr := fs.String("listen", envOr("HSM_LISTEN_ADDR", "127.0.0.1:8090"), "HTTP listen address (loopback recommended)")
	disableUI := fs.Bool("disable-ui", envBool("HSM_DISABLE_UI"), "disable built-in HTML UI and serve /api/v1/* only")
	secureCookies := fs.Bool("secure-cookies", envBool("HSM_SECURE_COOKIES"), "set Secure attribute on session cookies (enable behind HTTPS)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	var authMgr *auth.Manager
	if *authPath != "" {
		authCfg, err := auth.LoadConfig(*authPath)
		if err != nil {
			return err
		}
		authMgr = auth.NewManager(authCfg)
	}
	if !cfg.ReadOnly && (authMgr == nil || !authMgr.Enabled()) {
		return fmt.Errorf("writable configuration (read_only=false) requires --auth-config with at least one user or token")
	}

	snapSvc := snapshot.NewService(cfg, snapshot.NewUnixBrokerClient(*sockPath), hostmetrics.NewReader())
	webSrv, err := web.NewServer(snapSvc, web.Options{
		DisableUI:     *disableUI,
		Auth:          authMgr,
		SecureCookies: *secureCookies,
	})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           webSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Web/API listening on http://%s (ui_enabled=%v, broker=unix://%s)", *listenAddr, !*disableUI, *sockPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func defaultSocketPath() string {
	return filepath.Join(os.TempDir(), "home-server-manager-broker.sock")
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envBool(name string) bool {
	v := os.Getenv(name)
	return v == "1" || v == "true" || v == "TRUE"
}
