package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the actual compiled executables, not just their shared run functions.
func TestExecutableFailureExitCodes(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, command := range []string{"agent", "upload", "cleanup", "auth-check"} {
		t.Run(command, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), command)
			build := exec.Command("go", "build", "-o", binary, "./cmd/"+command)
			build.Dir = root
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build failed: %v %s", err, output)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			run := exec.CommandContext(ctx, binary)
			run.Env = []string{"PATH=" + os.Getenv("PATH"), "AZURE_DOCUMENTDB_DATABASENAME=Hotels"}
			output, err := run.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "explicit disposable scope required") || strings.Contains(string(output), "succeeded") {
				t.Fatalf("expected fail-closed exit 1, got %v %q", err, output)
			}
		})
	}
}
