package main

import (
	"testing"

	"github.com/justmao945/omp-im/internal/config"
)

func TestFindWeixinPlatform(t *testing.T) {
	cfg := &config.Config{
		Platforms: []config.PlatformConfig{
			{Name: "work", Type: "weixin", Options: map[string]any{}},
			{Name: "personal", Type: "weixin", Options: map[string]any{}},
			{Type: "wecom", Options: map[string]any{"bot_id": "b", "secret": "s"}},
		},
	}

	pc, err := findWeixinPlatform(cfg, "work")
	if err != nil {
		t.Fatalf("find work: %v", err)
	}
	if pc.Name != "work" {
		t.Fatalf("expected work, got %q", pc.Name)
	}

	if _, err := findWeixinPlatform(cfg, "missing"); err == nil {
		t.Fatal("expected error for missing account")
	}

	if _, err := findWeixinPlatform(cfg, ""); err == nil {
		t.Fatal("expected error when multiple weixin accounts and no name given")
	}
}

func TestFindWeixinPlatformSingleDefault(t *testing.T) {
	cfg := &config.Config{
		Platforms: []config.PlatformConfig{
			{Type: "weixin", Options: map[string]any{}},
		},
	}
	pc, err := findWeixinPlatform(cfg, "")
	if err != nil {
		t.Fatalf("find single default: %v", err)
	}
	if pc.WeixinAccount() != "default" {
		t.Fatalf("expected default, got %q", pc.WeixinAccount())
	}
}

func TestCloneOptions(t *testing.T) {
	orig := map[string]any{"a": 1, "b": "two"}
	cloned := cloneOptions(orig)
	if len(cloned) != len(orig) {
		t.Fatalf("cloned length mismatch")
	}
	cloned["a"] = 99
	if orig["a"] != 1 {
		t.Fatal("clone mutated original")
	}
}
