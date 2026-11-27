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
	case "auth-init":
		if err := runAuthInit(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <broker|web|auth-init> [flags]\n", filepath.Base(os.Args[0]))
}

func runAuthInit(args []string) error {
	fs := flag.NewFlagSet("auth-init", flag.ExitOnError)
	path := fs.String("auth-config", "", "path to a new managed auth JSON file in a private directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("auth-init requires --auth-config")
	}
	code, expires, err := auth.InitManagedConfig(*path)
	if err != nil {
		return fmt.Errorf("initialize account setup: %w", err)
	}
	fmt.Printf("One-time setup code (expires %s): %s\n", expires.Format(time.RFC3339), code)
	return nil
}

func runBroker(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("broker", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("HSM_CONFIG", "fixtures/manager.json"), "path to host manager config JSON")
	sockPath := fs.String("socket", envOr("HSM_BROKER_SOCKET", defaultSocketPath()), "Unix domain socket path to listen on")
	setup := fs.Bool("setup", envBool("HSM_SETUP"), "enable the container watchlist")
	watchPath := fs.String("watchlist", envOr("HSM_WATCHLIST", ""), "path to the persisted dashboard watchlist")
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
	if *setup {
		if *watchPath == "" {
			return fmt.Errorf("container setup requires --watchlist")
		}
		if err := b.LoadWatchlist(*watchPath); err != nil {
			return err
		}
	}
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
	setup := fs.Bool("setup", envBool("HSM_SETUP"), "enable container setup")
	secureCookies := fs.Bool("secure-cookies", envBool("HSM_SECURE_COOKIES"), "set Secure attribute on session cookies (enable behind HTTPS)")
	manageAuth := fs.Bool("manage-auth", envBool("HSM_MANAGE_AUTH"), "enable browser account setup and password changes")
	tlsCert := fs.String("tls-cert", envOr("HSM_TLS_CERT", ""), "path to the HTTPS certificate and chain")
	tlsKey := fs.String("tls-key", envOr("HSM_TLS_KEY", ""), "path to the HTTPS private key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return fmt.Errorf("HTTPS requires both --tls-cert and --tls-key")
	}
	usingTLS := *tlsCert != ""
	if *manageAuth && (!usingTLS || *authPath == "" || *disableUI) {
		return fmt.Errorf("managed auth requires HTTPS, --auth-config, and the web UI")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	var authMgr *auth.Manager
	if *manageAuth {
		authMgr, err = auth.NewManagedManager(*authPath)
		if err != nil {
			return err
		}
	} else if *authPath != "" {
		authCfg, err := auth.LoadConfig(*authPath)
		if err != nil {
			return err
		}
		if authCfg.Bootstrap != nil {
			return fmt.Errorf("a setup code requires --manage-auth and HTTPS")
		}
		authMgr = auth.NewManager(authCfg)
		if !authMgr.Enabled() {
			return fmt.Errorf("the supplied auth config requires at least one user or token")
		}
	}
	if !cfg.ReadOnly && (authMgr == nil || !authMgr.Enabled()) {
		return fmt.Errorf("writable configuration (read_only=false) requires --auth-config with at least one user or token")
	}

	brokerClient := snapshot.NewUnixBrokerClient(*sockPath)
	snapSvc := snapshot.NewService(cfg, brokerClient, hostmetrics.NewReader())
	opts := web.Options{
		DisableUI: *disableUI, Auth: authMgr, SecureCookies: *secureCookies || usingTLS,
		LocalSetup: *setup && cfg.ReadOnly && !authMgr.Enabled(),
	}
	if *setup && !*disableUI {
		opts.Discovery = brokerClient
	}
	webSrv, err := web.NewServer(snapSvc, opts)
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
		scheme := "http"
		serve := srv.ListenAndServe
		if usingTLS {
			scheme = "https"
			serve = func() error { return srv.ListenAndServeTLS(*tlsCert, *tlsKey) }
		}
		log.Printf("Web/API listening on %s://%s (ui_enabled=%v, broker=unix://%s)", scheme, *listenAddr, !*disableUI, *sockPath)
		if err := serve(); err != nil && err != http.ErrServerClosed {
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
