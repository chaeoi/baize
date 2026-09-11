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
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"baize/agent/internal/agent"
	"baize/agent/internal/collector"
	"baize/agent/internal/config"
	"baize/agent/internal/service"
	"baize/shared/model"
	"baize/shared/rawstream"
)

var version = "dev"

type updateHandoff struct {
	ready    chan error
	finished chan error
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "service" {
		executablePath, err := os.Executable()
		if err != nil {
			slog.Error("resolve executable path", "error", err)
			os.Exit(1)
		}
		if err := service.Execute(os.Args[2:], executablePath); err != nil {
			slog.Error("service command", "error", err)
			os.Exit(1)
		}
		return
	}
	flags := flag.NewFlagSet("baize-agent", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: baize-agent [run] [options]")
		fmt.Fprintln(os.Stderr, "       baize-agent service <install|uninstall|status>")
		flags.PrintDefaults()
	}
	configPath := flags.String("config", "/opt/baize/agent/config.yml", "Agent configuration file")
	showVersion := flags.Bool("version", false, "print version and exit")
	checkConfig := flags.Bool("check-config", false, "validate configuration and built-in robot model, then exit")
	arguments := os.Args[1:]
	supervise := len(arguments) > 0 && arguments[0] == "supervise"
	if supervise {
		arguments = arguments[1:]
	}
	if len(arguments) > 0 && arguments[0] == "run" {
		arguments = arguments[1:]
	}
	flags.Parse(arguments)
	if flags.NArg() != 0 {
		slog.Error("unknown command", "command", flags.Arg(0))
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println(version)
		return
	}
	if supervise {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := agent.Supervise(ctx, "/opt/baize/agent/bin/baize-agent", *configPath); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Agent supervisor stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load configuration", "error", err, "path", *configPath)
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
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	managedSubscriber := os.Getenv("BAIZE_ROS2_SUBSCRIBER") == ""
	ros2Subscriber, err := service.PrepareROS2Subscriber()
	if err != nil {
		slog.Warn("prepare ROS2 subscriber", "error", err)
	} else if err := os.Setenv("BAIZE_ROS2_SUBSCRIBER", ros2Subscriber); err != nil {
		return fmt.Errorf("configure ROS2 subscriber: %w", err)
	}
	if managedSubscriber {
		_ = os.Setenv("BAIZE_MANAGED_SUBSCRIBER", "1")
	}
	hostname, _ := os.Hostname()
	httpClient := &http.Client{Timeout: cfg.Agent.HTTPTimeout.Value()}
	dashboardClient := agent.NewClient(cfg.Agent.DashboardURL, cfg.Agent.Token, httpClient)
	directory := os.Getenv("STATE_DIRECTORY")
	if directory == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		directory = filepath.Join(cache, "baize-agent", cfg.Agent.UUID)
	}
	outbox, err := agent.NewRawOutbox(agent.RawCachePath(directory), cfg.Agent.RawCacheBytes)
	if err != nil {
		return fmt.Errorf("open raw outbox: %w", err)
	}
	if err := agent.ConfirmUpdate(); err != nil {
		slog.Warn("confirm Agent startup", "error", err)
	}
	sourceCtx, stopSources := context.WithCancel(ctx)
	defer stopSources()
	records := make(chan rawstream.Record, 256)
	var sources sync.WaitGroup
	startTopic := func(topic, messageType string, setup []string, environment map[string]string, user string) {
		sources.Add(1)
		go func() {
			defer sources.Done()
			for sourceCtx.Err() == nil {
				err := collector.StreamRawTopic(sourceCtx, setup, environment, user, topic, messageType, records)
				if sourceCtx.Err() != nil {
					return
				}
				slog.Warn("ROS2 raw topic stream stopped", "topic", topic, "error", err)
				timer := time.NewTimer(time.Second)
				select {
				case <-sourceCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	if cfg.Motor.Enabled {
		startTopic(cfg.Motor.Topic, cfg.Motor.MessageType, cfg.Motor.ROSSetup, cfg.Motor.ROSEnvironment, cfg.Motor.ROSUser)
	}
	if cfg.BMS.Enabled {
		startTopic(cfg.BMS.ROSTopic, cfg.BMS.ROSMessageType, cfg.BMS.ROSSetup, cfg.BMS.ROSEnvironment, cfg.BMS.ROSUser)
	}
	sources.Add(1)
	go func() {
		defer sources.Done()
		systemCollector := collector.NewSystemCollector()
		ticker := time.NewTicker(cfg.Agent.ReportInterval.Value())
		defer ticker.Stop()
		for sourceCtx.Err() == nil {
			at := time.Now().UTC()
			var snapshot model.Telemetry
			if cfg.System.Enabled {
				metrics, err := systemCollector.Collect(cfg.System.DiskPaths)
				if err != nil {
					snapshot.Errors = append(snapshot.Errors, model.ComponentError{Component: "system", Message: err.Error(), At: at})
				}
				snapshot.System = &metrics
			}
			if cfg.GPU.Enabled {
				metrics, err := collector.CollectNVIDIAGPUs(cfg.GPU.Command, cfg.GPU.Timeout.Value())
				if err != nil && !errors.Is(err, collector.ErrNoGPU) {
					snapshot.Errors = append(snapshot.Errors, model.ComponentError{Component: "gpu", Message: err.Error(), At: at})
				}
				snapshot.GPUs = metrics
			}
			model.SanitizeFinite(&snapshot)
			record, err := rawstream.NewHostRecord(at, snapshot.System, snapshot.GPUs, snapshot.Errors...)
			if err != nil {
				slog.Warn("encode host sample", "error", err)
			} else {
				select {
				case records <- record:
				case <-sourceCtx.Done():
					return
				}
			}
			select {
			case <-sourceCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() { sources.Wait(); close(records) }()
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		outbox.Run(sourceCtx, dashboardClient)
	}()
	defer func() { stopSources(); <-senderDone }()

	// Size-triggered flushes bound memory even if topic rates grow. The limit
	// also guarantees that one compressed batch fits the configured cache.
	batchLimit := int(min(cfg.Agent.RawCacheBytes/2, 4<<20))
	pendingBytes := 0
	pending := make([]rawstream.Record, 0, 1024)
	updates := make(chan updateHandoff)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		sequence, err := outbox.ReserveSequence()
		if err != nil {
			return err
		}
		data, err := rawstream.Encode(rawstream.Batch{
			RobotUUID: cfg.Agent.UUID, RobotCode: cfg.Agent.RobotCode, RobotModel: cfg.Agent.RobotModel,
			Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, AgentVersion: version,
			Sequence: sequence, Records: pending,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(sequence, data); err != nil {
			return err
		}
		clear(pending)
		pending = pending[:0]
		pendingBytes = 0
		return nil
	}
	appendRecord := func(record rawstream.Record) error {
		record.ReceiveTimestamp = time.Now().UTC()
		size := len(record.Payload) + len(record.Topic) + len(record.MessageType) + len(record.Serialization) + 64
		if size > batchLimit {
			return fmt.Errorf("raw message on %s exceeds batch limit %d", record.Topic, batchLimit)
		}
		if pendingBytes+size > batchLimit || len(pending) >= 10_000 {
			if err := flush(); err != nil {
				return err
			}
		}
		pending = append(pending, record)
		pendingBytes += size
		return nil
	}
	if cfg.Update.Enabled && version != "dev" {
		go updateLoop(ctx, cfg, httpClient, updates)
	}
	ticker := time.NewTicker(cfg.Agent.RawBatchInterval.Value())
	defer ticker.Stop()
	for {
		select {
		case record, ok := <-records:
			if !ok {
				return flush()
			} // final batch stays in the durable outbox
			if err := appendRecord(record); err != nil {
				return err
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		case update := <-updates:
			stopSources()
			for record := range records {
				if err := appendRecord(record); err != nil {
					update.ready <- err
					return err
				}
			}
			<-senderDone
			err := flush()
			update.ready <- err
			if err != nil {
				return err
			}
			// A successful exec never returns. On failure let the supervisor
			// restart the old binary, with the pending data safely on disk.
			return <-update.finished
		}
	}
}

func updateLoop(ctx context.Context, cfg config.Config, httpClient *http.Client, updates chan<- updateHandoff) {
	client := agent.NewGitHubClient(httpClient)
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, cfg.Agent.HTTPTimeout.Value())
		defer cancel()
		update, err := client.Check(checkCtx, version, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			slog.Warn("check update", "error", err)
			return
		}
		if update == nil {
			return
		}
		slog.Info("prepare agent update", "from", version, "to", update.Version)
		handoff := updateHandoff{ready: make(chan error, 1), finished: make(chan error, 1)}
		err = agent.ApplyUpdate(ctx, client, *update, func() error {
			select {
			case updates <- handoff:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case err := <-handoff.ready:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		handoff.finished <- err
		if err != nil {
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
