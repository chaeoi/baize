package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"echobot/dashboard/backend/internal/dashboard"
)

var version = "dev"

func main() {
	listen := flag.String("listen", env("DASHBOARD_LISTEN", ":8080"), "dashboard listen address")
	dataDir := flag.String("data", env("DASHBOARD_DATA_DIR", "/opt/echobot/dashboard/data"), "persistent data directory")
	flag.Parse()
	agentToken := os.Getenv("DASHBOARD_AGENT_TOKEN")
	adminPassword := os.Getenv("DASHBOARD_ADMIN_PASSWORD")
	if len(agentToken) < 12 || len(adminPassword) < 12 {
		slog.Error("DASHBOARD_AGENT_TOKEN and DASHBOARD_ADMIN_PASSWORD must each contain at least 12 characters")
		os.Exit(2)
	}
	store, err := dashboard.NewStore(*dataDir)
	if err != nil {
		slog.Error("open dashboard store", "error", err)
		os.Exit(1)
	}
	handler := dashboard.NewServer(dashboard.ServerConfig{
		AgentToken: agentToken, AdminPassword: adminPassword,
		AllowedOrigin: os.Getenv("DASHBOARD_ALLOWED_ORIGIN"), FrontendDir: os.Getenv("DASHBOARD_FRONTEND_DIR"),
	}, store)
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	tlsCert, tlsKey := os.Getenv("DASHBOARD_TLS_CERT"), os.Getenv("DASHBOARD_TLS_KEY")
	if (tlsCert == "") != (tlsKey == "") {
		slog.Error("DASHBOARD_TLS_CERT and DASHBOARD_TLS_KEY must be configured together")
		os.Exit(2)
	}
	protocol := "http"
	if tlsCert != "" {
		protocol = "https"
	}
	slog.Info("dashboard listening", "address", *listen, "protocol", protocol, "version", version)
	var serveErr error
	if tlsCert != "" {
		serveErr = server.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		slog.Error("dashboard server", "error", serveErr)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
