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
	"baize/shared/robotmodel"
)

var version = "dev"

func main() {
	if err := robotmodel.Validate(); err != nil {
		slog.Error("validate embedded robot models", "error", err)
		os.Exit(2)
	}
	configPath := flag.String("config", "/dashboard/data/config.yaml", "Dashboard configuration file")
	checkConfig := flag.Bool("check-config", false, "validate Dashboard configuration and exit")
	flag.Parse()
	cfg, err := dashboardconfig.Load(*configPath)
	if err != nil {
		slog.Error("load Dashboard configuration", "error", err, "path", *configPath)
		os.Exit(2)
	}
	if *checkConfig {
		fmt.Println("config is valid")
		return
	}
	agentToken := cfg.Dashboard.AgentToken
	if agentToken == "" {
		agentToken, err = dashboardconfig.NewAgentToken()
		if err != nil {
			slog.Error("generate Dashboard agent token", "error", err)
			os.Exit(1)
		}
		if err := dashboardconfig.WriteAgentToken(*configPath, agentToken); err != nil {
			slog.Error("save Dashboard agent token", "error", err, "path", *configPath)
			os.Exit(1)
		}
		slog.Info("Dashboard agent token initialized in configuration", "path", *configPath)
	}
	store, err := dashboard.NewStore(cfg.Dashboard.DataDir, cfg.Dashboard.HistoryDataDir, dashboard.StoreOptions{
		AdminUser: dashboardconfig.AdminUsername, BootstrapPassword: dashboardconfig.DefaultAdminPassword,
		RequirePasswordChange: true,
		HistoryRetention:      cfg.Dashboard.HistoryRetention.Value(), HistorySampleInterval: cfg.Dashboard.HistorySampleInterval.Value(),
	})
	if err != nil {
		slog.Error("open dashboard store", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	jwtSecret, jwtSecretCreated, err := store.Secret("jwt_secret", 32)
	if err != nil {
		slog.Error("load Dashboard JWT secret", "error", err)
		os.Exit(1)
	}
	if jwtSecretCreated {
		slog.Warn("Dashboard control secrets initialized", "jwt_secret_created", true)
	}
	handler := dashboard.NewServer(dashboard.ServerConfig{
		AgentToken: agentToken, AdminUser: dashboardconfig.AdminUsername,
		JWTSecret: jwtSecret, FrontendDir: cfg.Dashboard.FrontendDir, CookieSecure: cfg.Dashboard.CookieSecure,
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
