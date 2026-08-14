package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dashboardconfig "baize/dashboard/backend/internal/config"
	"baize/dashboard/backend/internal/dashboard"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "/config/config.yml", "path to the dashboard YAML config")
	checkConfig := flag.Bool("check-config", false, "validate config and exit")
	flag.Parse()
	var cfg dashboardconfig.Config
	var generated dashboardconfig.GeneratedValues
	var err error
	if *checkConfig {
		cfg, err = dashboardconfig.Load(*configPath)
	} else {
		cfg, generated, err = dashboardconfig.LoadOrCreate(*configPath)
	}
	if err != nil {
		slog.Error("load dashboard config", "error", err, "path", *configPath)
		os.Exit(2)
	}
	if *checkConfig {
		fmt.Println("config is valid")
		return
	}
	if generated.AgentToken != "" || generated.AdminPassword != "" {
		slog.Warn("dashboard config values generated", "path", *configPath, "admin_user", cfg.Dashboard.AdminUser, "admin_password", generated.AdminPassword, "agent_token", generated.AgentToken, "jwt_secret_generated", generated.JWTSecret != "")
	}
	store, err := dashboard.NewStore(cfg.Dashboard.DataDir)
	if err != nil {
		slog.Error("open dashboard store", "error", err)
		os.Exit(1)
	}
	handler := dashboard.NewServer(dashboard.ServerConfig{
		AgentToken: cfg.Dashboard.AgentToken, AdminPassword: cfg.Dashboard.AdminPassword,
		AdminUser: cfg.Dashboard.AdminUser, JWTSecret: cfg.Dashboard.JWTSecret, FrontendDir: cfg.Dashboard.FrontendDir,
	}, store)
	server := &http.Server{Addr: cfg.Dashboard.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("dashboard listening", "address", cfg.Dashboard.Listen, "protocol", "http", "version", version)
	serveErr := server.ListenAndServe()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		slog.Error("dashboard server", "error", serveErr)
		os.Exit(1)
	}
}
