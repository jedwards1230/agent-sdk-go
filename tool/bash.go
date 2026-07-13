package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/jedwards1230/agent-sdk-go/spill"
)

// defaultBashTimeout is the internal timeout applied when a bash call omits
// timeout_ms.
const defaultBashTimeout = 120_000 * time.Millisecond

// maxBashTimeout caps timeout_ms regardless of what the model requests.
const maxBashTimeout = 600_000 * time.Millisecond

// Bash runs a shell command in a working directory. It is policy-free: no
// workspace confinement or command allow/deny list is enforced here — that is
// a permission-layer concern (M3).
type Bash struct {
	root  string
	shell string
}

// NewBash returns a Bash that runs commands in root. The shell is resolved
// once at construction: bash if it is on PATH, else /bin/sh.
func NewBash(root string) *Bash {
	shell, err := exec.LookPath("bash")
	if err != nil {
		shell = "/bin/sh"
	}
	return &Bash{root: root, shell: shell}
}

// Name returns "bash".
func (b *Bash) Name() string { return "bash" }

// Description returns the model-facing description of Bash.
func (b *Bash) Description() string {
	return "Run a shell command in the working directory and return its combined " +
		"stdout and stderr. Long-running commands may be cut off by a timeout."
}

// Spec returns the JSON Schema for Bash's input.
func (b *Bash) Spec() Schema {
	return ObjectSchema([]string{"command"}, map[string]Property{
		"command": {
			Type:        "string",
			Description: "The shell command to execute.",
		},
		"timeout_ms": {
			Type:        "integer",
			Description: "Maximum time to allow the command to run, in milliseconds (default 120000, max 600000).",
		},
	})
}

// bashInput is the decoded shape of Bash's Run argument.
type bashInput struct {
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeout_ms"`
}

// Run executes the command via "<shell> -c <command>" with cmd.Dir set to
// root, streaming the process's combined stdout+stderr straight to the per-call
// spill sink so its full output is never buffered in memory. The sink is taken
// from ctx ([spill.FromContext]) when the loop provides one; on a direct call it
// falls back to a file-less bounded sink whose head+tail excerpt is returned as
// Content. When the loop provides the sink Run returns empty Content — the loop
// derives the model-facing excerpt from the same sink. An exit-status, timeout,
// or start-failure footer is written into the sink so it appears in the spill
// file and the excerpt.
//
// Cancelling ctx SIGKILLs the subprocess and Run returns ctx.Err() (partial
// output already streamed is durable). An internal timeout (timeout_ms, default
// 120s, capped at 600s) yields a [Result] with IsError set rather than an error
// return. If the process never starts (e.g. root does not exist or the shell is
// missing), the underlying OS reason is written into the sink rather than dropped.
func (b *Bash) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in bashInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}
	if in.Command == "" {
		return Result{}, fmt.Errorf("tool: bash: command is required")
	}

	timeout := defaultBashTimeout
	if in.TimeoutMS > 0 {
		timeout = time.Duration(in.TimeoutMS) * time.Millisecond
		if timeout > maxBashTimeout {
			timeout = maxBashTimeout
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, b.shell, "-c", in.Command) // #nosec G204 -- bash is the tool's job, not a vulnerability here
	cmd.Dir = b.root
	configureProcessGroup(cmd)

	// Stream process output straight to the spill sink. os/exec guarantees a
	// single goroutine writes when Stdout and Stderr are the same value, so the
	// sink needs no locking. A direct caller (no loop sink) gets a file-less
	// bounded sink and reads the excerpt back as Content.
	sink, provided := spill.FromContext(ctx)
	var local *spill.Writer
	if !provided {
		local, _ = spill.Create("", "", "") // file-less: never errors
		sink = local
	}
	cmd.Stdout = sink
	cmd.Stderr = sink

	runErr := cmd.Run()

	// The external ctx being cancelled always aborts the call, even if the
	// internal timeout also expired at the same moment. Whatever streamed before
	// the kill is already durably in the append-only sink.
	if ctx.Err() != nil {
		if local != nil {
			_, _ = local.Close()
		}
		return Result{}, ctx.Err()
	}

	md, isErr := footer(sink, cmd, runCtx, runErr, timeout)

	content := ""
	if local != nil {
		ref, _ := local.Close()
		content = ref.Excerpt
		md.Truncated = ref.Elided
	}
	return Result{Content: content, IsError: isErr, Metadata: md}, nil
}

// footer resolves the command's outcome, writes a human footer into the sink so
// it lands in the spill file (and the excerpt), and returns the result metadata
// and whether the call is an error.
func footer(sink io.Writer, cmd *exec.Cmd, runCtx context.Context, runErr error, timeout time.Duration) (md Metadata, isErr bool) {
	switch {
	case runCtx.Err() != nil:
		// Internal timeout fired; parent ctx is still live (checked by caller).
		_, _ = fmt.Fprintf(sink, "\n… command timed out after %s", timeout)
		return Metadata{}, true
	case cmd.ProcessState != nil:
		exitCode := cmd.ProcessState.ExitCode()
		if exitCode != 0 {
			_, _ = fmt.Fprintf(sink, "\n[exit %d]", exitCode)
		}
		return Metadata{ExitCode: &exitCode}, exitCode != 0
	case runErr != nil:
		// The process never started (ProcessState is nil) — e.g. cmd.Dir does not
		// exist or the shell is missing. cmd.Run's error carries the real reason;
		// surface it instead of a bare "[exit -1]".
		exitCode := -1
		_, _ = fmt.Fprintf(sink, "\ncommand failed to start: %v", runErr)
		return Metadata{ExitCode: &exitCode}, true
	default:
		exitCode := 0
		return Metadata{ExitCode: &exitCode}, false
	}
}
