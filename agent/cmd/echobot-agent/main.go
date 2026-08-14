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
	"runtime"
	"sync"
	"syscall"
	"time"

	"echobot/agent/internal/agent"
	"echobot/agent/internal/collector"
	"echobot/agent/internal/config"
	"echobot/shared/model"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.yml", "path to the agent YAML config")
	showVersion := flag.Bool("version", false, "print version and exit")
	checkConfig := flag.Bool("check-config", false, "validate config and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "error", err, "path", *configPath)
		os.Exit(2)
	}
	if *checkConfig {
		fmt.Println("config is valid")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	hostname, _ := os.Hostname()
	httpClient := &http.Client{Timeout: cfg.Agent.HTTPTimeout.Value()}
	dashboardClient := agent.NewClient(cfg.Agent.DashboardURL, cfg.Agent.Token, httpClient)
	systemCollector := collector.NewSystemCollector()
	var motorCollector *collector.MotorCollector
	if cfg.Motor.Enabled {
		motorCollector = collector.NewMotorCollector(cfg.Motor)
	}
	var bmsCollector *collector.BMSCollector
	if cfg.BMS.Enabled {
		bmsCollector = collector.NewBMSCollector(cfg.BMS)
		go bmsCollector.Run(ctx)
		if cfg.BMS.PublishROS2 {
			go publishBMSLoop(ctx, cfg.BMS, bmsCollector)
		}
	}
	if cfg.Update.Enabled && version != "dev" {
		go updateLoop(ctx, cfg, dashboardClient)
	}

	identity := model.Robot{UUID: cfg.Agent.UUID, Code: cfg.Agent.RobotCode, Model: cfg.Agent.RobotModel, Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH}
	ticker := time.NewTicker(cfg.Agent.ReportInterval.Value())
	defer ticker.Stop()
	for {
		started := time.Now()
		telemetry := collect(ctx, cfg, identity, systemCollector, motorCollector, bmsCollector)
		reportCtx, cancel := context.WithTimeout(ctx, cfg.Agent.HTTPTimeout.Value())
		err := dashboardClient.Report(reportCtx, telemetry)
		cancel()
		if err != nil {
			slog.Warn("report telemetry", "error", err)
		}
		slog.Debug("collection complete", "duration", time.Since(started))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func collect(ctx context.Context, cfg config.Config, robot model.Robot, systemCollector *collector.SystemCollector, motorCollector *collector.MotorCollector, bmsCollector *collector.BMSCollector) model.Telemetry {
	result := model.Telemetry{SchemaVersion: model.SchemaVersion, Robot: robot, AgentVersion: version, CollectedAt: time.Now().UTC()}
	var mu sync.Mutex
	var wait sync.WaitGroup
	addError := func(component string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		result.Errors = append(result.Errors, model.ComponentError{Component: component, Message: err.Error(), At: time.Now().UTC()})
		mu.Unlock()
	}
	if cfg.System.Enabled {
		wait.Add(1)
		go func() {
			defer wait.Done()
			metrics, err := systemCollector.Collect(cfg.System.DiskPaths)
			mu.Lock()
			result.System = &metrics
			mu.Unlock()
			addError("system", err)
		}()
	}
	if cfg.GPU.Enabled {
		wait.Add(1)
		go func() {
			defer wait.Done()
			metrics, err := collector.CollectNVIDIAGPUs(cfg.GPU.Command, cfg.GPU.Timeout.Value())
			if err == nil {
				mu.Lock()
				result.GPUs = metrics
				mu.Unlock()
			} else if !errors.Is(err, collector.ErrNoGPU) {
				addError("gpu", err)
			}
		}()
	}
	if motorCollector != nil {
		wait.Add(1)
		go func() {
			defer wait.Done()
			metrics, err := motorCollector.Collect(ctx)
			mu.Lock()
			result.Motors = &metrics
			mu.Unlock()
			addError("motor", err)
		}()
	}
	if bmsCollector != nil {
		metrics, err := bmsCollector.Snapshot()
		result.BMS = &metrics
		addError("bms", err)
	}
	wait.Wait()
	return result
}

func publishBMSLoop(ctx context.Context, cfg config.BMSConfig, bms *collector.BMSCollector) {
	ticker := time.NewTicker(cfg.PublishInterval.Value())
	defer ticker.Stop()
	for {
		metrics, _ := bms.Snapshot()
		if metrics.Online {
			err := collector.PublishBMSROS2(ctx, cfg, metrics)
			bms.MarkPublished(err == nil)
			if err != nil {
				slog.Warn("publish BMS ROS2 topic", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func updateLoop(ctx context.Context, cfg config.Config, client *agent.Client) {
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, cfg.Agent.HTTPTimeout.Value())
		defer cancel()
		update, err := client.CheckUpdate(checkCtx, cfg.Agent.UUID, version, runtime.GOOS, runtime.GOARCH, cfg.Update.Automatic)
		if err != nil {
			slog.Warn("check update", "error", err)
			return
		}
		if update == nil {
			return
		}
		slog.Info("applying agent update", "from", version, "to", update.Version)
		if err := agent.ApplyUpdate(ctx, client, *update); err != nil {
			slog.Error("apply update", "error", err)
		}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		check()
	}
	ticker := time.NewTicker(cfg.Update.CheckInterval.Value())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}
