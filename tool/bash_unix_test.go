//go:build unix

package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBashKillsProcessGroupOnTimeout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	b := NewBash(dir)
	// Background a long-lived child, record its PID, then block in the
	// foreground so the internal timeout fires while the child is alive.
	command := fmt.Sprintf("sleep 30 & echo $! > %s; sleep 30", pidFile)
	input, err := json.Marshal(map[string]any{"command": command, "timeout_ms": 300})
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (timeout)")
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", data, err)
	}

	// The backgrounded child must be dead once the group was killed. Poll
	// briefly for reaping (kill(pid, 0) → ESRCH when gone).
	dead := false
	for range 40 {
		if err := syscall.Kill(pid, 0); err != nil {
			dead = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !dead {
		t.Fatalf("backgrounded child %d survived the process-group kill", pid)
	}
}
