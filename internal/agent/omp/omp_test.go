package omp

import (
	"testing"
)

func TestResolveOMPCommand(t *testing.T) {
	cmd, args := resolveOMPCommand()
	if cmd != "omp" {
		t.Fatalf("command = %q, want omp", cmd)
	}
	if len(args) != 1 || args[0] != "acp" {
		t.Fatalf("args = %v, want [acp]", args)
	}
}
