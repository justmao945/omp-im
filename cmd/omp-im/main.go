// Command omp-im wires Weixin (and eventually WeCom) to configurable agents.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/justmao945/omp-im/internal/agent"
	"github.com/justmao945/omp-im/internal/config"
	"github.com/justmao945/omp-im/internal/core"
	"github.com/justmao945/omp-im/internal/platform/http"
	"github.com/justmao945/omp-im/internal/platform/wecom"
	"github.com/justmao945/omp-im/internal/platform/weixin"
)

func main() {
	var (
		configPath = flag.String("config", config.DefaultPath(), "path to config.json")
		logLevel   = flag.String("log-level", "info", "log level: debug|info|warn|error")
	)
	flag.CommandLine.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [command]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  omp-im                 Run the server (default)")
		fmt.Fprintln(os.Stderr, "  omp-im weixin login [account]    Perform Weixin QR-code login")
		fmt.Fprintln(os.Stderr, "  omp-im weixin logout [account]   Remove saved Weixin session")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	setupLogger(*logLevel)

	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "weixin":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: omp-im weixin login|logout [account]")
				os.Exit(1)
			}
			switch args[1] {
			case "login":
				account := ""
				if len(args) > 2 {
					account = args[2]
				}
				if err := runWeixinLogin(*configPath, account); err != nil {
					slog.Error("weixin login failed", "error", err)
					os.Exit(1)
				}
				return
			case "logout":
				account := ""
				if len(args) > 2 {
					account = args[2]
				}
				if err := runWeixinLogout(*configPath, account); err != nil {
					slog.Error("weixin logout failed", "error", err)
					os.Exit(1)
				}
				return
			default:
				fmt.Fprintln(os.Stderr, "usage: omp-im weixin login|logout [account]")
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

func runWeixinLogin(configPath, account string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pc, err := findWeixinPlatform(cfg, account)
	if err != nil {
		return err
	}
	opts := cloneOptions(pc.Options)
	if pc.Name != "" {
		opts["name"] = pc.Name
	}
	return weixin.Login(context.Background(), opts)
}

func runWeixinLogout(configPath, account string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pc, err := findWeixinPlatform(cfg, account)
	if err != nil {
		return err
	}
	opts := cloneOptions(pc.Options)
	if pc.Name != "" {
		opts["name"] = pc.Name
	}
	return weixin.Logout(opts)
}

// findWeixinPlatform selects the Weixin platform entry matching the given
// account name. If account is empty, it returns the single configured Weixin
// platform or an error when there are multiple.
func findWeixinPlatform(cfg *config.Config, account string) (config.PlatformConfig, error) {
	var matches []config.PlatformConfig
	for _, pc := range cfg.Platforms {
		if pc.Type != "weixin" {
			continue
		}
		if account == "" || pc.WeixinAccount() == account {
			matches = append(matches, pc)
		}
	}
	if len(matches) == 0 {
		if account == "" {
			return config.PlatformConfig{}, fmt.Errorf("no weixin platform configured")
		}
		return config.PlatformConfig{}, fmt.Errorf("no weixin platform named %q", account)
	}
	if account == "" && len(matches) > 1 {
		var names []string
		for _, pc := range matches {
			names = append(names, pc.WeixinAccount())
		}
		return config.PlatformConfig{}, fmt.Errorf("multiple weixin accounts configured (%s); specify one by name", strings.Join(names, ", "))
	}
	return matches[0], nil
}

func cloneOptions(opts map[string]any) map[string]any {
	out := make(map[string]any, len(opts))
	for k, v := range opts {
		out[k] = v
	}
	return out
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

	for _, pc := range cfg.Projects {
		if pc.WorkDir == "" {
			continue
		}
		if err := os.MkdirAll(pc.WorkDir, 0o755); err != nil {
			return fmt.Errorf("create project work_dir %s: %w", pc.WorkDir, err)
		}
	}

	projects := make(map[string]core.Project, len(cfg.Projects))
	for _, pc := range cfg.Projects {
		projects[pc.Name] = core.Project{Name: pc.Name, WorkDir: pc.WorkDir}
	}

	engine := core.NewEngine(agents, cfg.Defaults.Agent, projects, cfg.Defaults.Project)
	if err := engine.SetSessionStore(cfg.SessionStorePath()); err != nil {
		return fmt.Errorf("load session store: %w", err)
	}

	for i, pc := range cfg.Platforms {
		switch pc.Type {
		case "weixin":
			opts := cloneOptions(pc.Options)
			if pc.Name != "" {
				opts["name"] = pc.Name
			}
			p, err := weixin.New(opts)
			if err != nil {
				return fmt.Errorf("create weixin platform %d: %w", i, err)
			}
			engine.AddPlatform(p)
		case "wecom":
			p, err := wecom.New(pc.Options)
			if err != nil {
				return fmt.Errorf("create wecom platform %d: %w", i, err)
			}
			engine.AddPlatform(p)
		case "http":
			p, err := http.New(pc.Options)
			if err != nil {
				return fmt.Errorf("create http platform %d: %w", i, err)
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
