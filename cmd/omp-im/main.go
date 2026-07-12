// Command omp-im wires Weixin (and eventually WeCom) to configurable agents.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/justmao945/omp-im/internal/agent"
	"github.com/justmao945/omp-im/internal/config"
	"github.com/justmao945/omp-im/internal/core"
	"github.com/justmao945/omp-im/internal/platform/weixin"
)

func main() {
	var (
		configPath = flag.String("config", defaultConfigPath(), "path to config.json")
		logLevel   = flag.String("log-level", "info", "log level: debug|info|warn|error")
	)
	flag.Parse()

	setupLogger(*logLevel)

	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "weixin":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: omp-im weixin login|logout")
				os.Exit(1)
			}
			switch args[1] {
			case "login":
				if err := runWeixinLogin(*configPath); err != nil {
					slog.Error("weixin login failed", "error", err)
					os.Exit(1)
				}
				return
			case "logout":
				if err := runWeixinLogout(*configPath); err != nil {
					slog.Error("weixin logout failed", "error", err)
					os.Exit(1)
				}
				return
			default:
				fmt.Fprintln(os.Stderr, "usage: omp-im weixin login|logout")
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
			os.Exit(1)
		}
	}

	if err := runServer(*configPath); err != nil {
		slog.Error("engine exited", "error", err)
		os.Exit(1)
	}
}

func runWeixinLogin(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, pc := range cfg.Platforms {
		if pc.Type == "weixin" {
			return weixin.Login(context.Background(), pc.Options)
		}
	}
	return fmt.Errorf("no weixin platform configured")
}

func runWeixinLogout(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, pc := range cfg.Platforms {
		if pc.Type == "weixin" {
			return weixin.Logout(pc.Options)
		}
	}
	return fmt.Errorf("no weixin platform configured")
}

func runServer(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	agents := make(map[string]core.Agent, len(cfg.Agents))
	for _, name := range cfg.Agents {
		a, err := agent.New(name)
		if err != nil {
			return fmt.Errorf("create agent %s: %w", name, err)
		}
		agents[name] = a
	}

	projects := make(map[string]core.Project, len(cfg.Projects))
	for _, pc := range cfg.Projects {
		projects[pc.Name] = core.Project{Name: pc.Name, WorkDir: pc.WorkDir}
	}

	engine := core.NewEngine(agents, cfg.Defaults.Agent, projects, cfg.Defaults.Project)

	for i, pc := range cfg.Platforms {
		switch pc.Type {
		case "weixin":
			p, err := weixin.New(pc.Options)
			if err != nil {
				return fmt.Errorf("create weixin platform %d: %w", i, err)
			}
			engine.AddPlatform(p)
		default:
			return fmt.Errorf("unsupported platform %d: %s", i, pc.Type)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("shutting down")
		_ = engine.Stop()
	}()

	var names []string
	for n := range agents {
		names = append(names, n)
	}
	slog.Info("omp-im running", "agents", names, "platforms", len(cfg.Platforms))
	return engine.Run()
}

func setupLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "info":
		lv = slog.LevelInfo
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})))
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".omp-im", "config.json")
	}
	return filepath.Join(home, ".omp-im", "config.json")
}
