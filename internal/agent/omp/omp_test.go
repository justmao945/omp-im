package omp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadShebang(t *testing.T) {
	tmp := t.TempDir()

	t.Run("has shebang", func(t *testing.T) {
		path := filepath.Join(tmp, "with-shebang")
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bun\n// code\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := readShebang(path)
		if err != nil {
			t.Fatalf("readShebang: %v", err)
		}
		if got != "/usr/bin/env bun" {
			t.Fatalf("shebang = %q, want /usr/bin/env bun", got)
		}
	})

	t.Run("no shebang", func(t *testing.T) {
		path := filepath.Join(tmp, "no-shebang")
		if err := os.WriteFile(path, []byte("// code\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := readShebang(path)
		if err != nil {
			t.Fatalf("readShebang: %v", err)
		}
		if got != "" {
			t.Fatalf("shebang = %q, want empty", got)
		}
	})
}

func TestShebangUsesBun(t *testing.T) {
	cases := []struct {
		shebang string
		want    bool
	}{
		{"/usr/bin/env bun", true},
		{"/usr/bin/env -S bun --flag", true},
		{"/Users/just/.bun/bin/bun", true},
		{"/usr/bin/env node", false},
		{"/bin/sh", false},
		{"", false},
	}
	for _, c := range cases {
		if got := shebangUsesBun(c.shebang); got != c.want {
			t.Fatalf("shebangUsesBun(%q) = %v, want %v", c.shebang, got, c.want)
		}
	}
}

func TestShebangBunArgs(t *testing.T) {
	cases := []struct {
		shebang string
		want    []string
	}{
		{"/usr/bin/env bun", nil},
		{"/usr/bin/env -S bun --flag", []string{"--flag"}},
		{"/path/to/bun --hot", []string{"--hot"}},
	}
	for _, c := range cases {
		got := shebangBunArgs(c.shebang)
		if len(got) != len(c.want) {
			t.Fatalf("shebangBunArgs(%q) = %v, want %v", c.shebang, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("shebangBunArgs(%q) = %v, want %v", c.shebang, got, c.want)
			}
		}
	}
}

func TestFindBun(t *testing.T) {
	tmp := t.TempDir()
	bunPath := filepath.Join(tmp, "bun")
	if runtime.GOOS == "windows" {
		bunPath += ".exe"
	}
	if err := os.WriteFile(bunPath, []byte("fake bun"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("finds bun in PATH", func(t *testing.T) {
		t.Setenv("PATH", tmp)
		got, err := findBun(filepath.Join(tmp, "omp"))
		if err != nil {
			t.Fatalf("findBun: %v", err)
		}
		if got != bunPath {
			t.Fatalf("findBun = %q, want %q", got, bunPath)
		}
	})

	t.Run("finds bun next to omp", func(t *testing.T) {
		t.Setenv("PATH", "")
		got, err := findBun(filepath.Join(tmp, "omp"))
		if err != nil {
			t.Fatalf("findBun: %v", err)
		}
		if got != bunPath {
			t.Fatalf("findBun = %q, want %q", got, bunPath)
		}
	})

	t.Run("returns error when missing", func(t *testing.T) {
		t.Setenv("PATH", "")
		dir := t.TempDir()
		if _, err := findBun(filepath.Join(dir, "omp")); err == nil {
			t.Fatal("expected error when bun is missing")
		}
	})
}

func TestResolveOMPCommand(t *testing.T) {
	tmp := t.TempDir()

	ompPath := filepath.Join(tmp, "omp")
	if err := os.WriteFile(ompPath, []byte("#!/usr/bin/env bun\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bunPath := filepath.Join(tmp, "bun")
	if runtime.GOOS == "windows" {
		bunPath += ".exe"
	}
	if err := os.WriteFile(bunPath, []byte("fake bun"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", tmp)
	cmd, args := resolveOMPCommand()
	if cmd != bunPath {
		t.Fatalf("command = %q, want %q", cmd, bunPath)
	}
	if len(args) != 2 || args[0] != ompPath || args[1] != "acp" {
		t.Fatalf("args = %v, want [%q acp]", args, ompPath)
	}
}

func TestResolveOMPCommandWithoutBunShebang(t *testing.T) {
	tmp := t.TempDir()

	ompPath := filepath.Join(tmp, "omp")
	if err := os.WriteFile(ompPath, []byte("#!/bin/sh\necho native\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", tmp)
	cmd, args := resolveOMPCommand()
	if cmd != "omp" {
		t.Fatalf("command = %q, want omp", cmd)
	}
	if strings.Join(args, " ") != "acp" {
		t.Fatalf("args = %v, want [acp]", args)
	}
}
