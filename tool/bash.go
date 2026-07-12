package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
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
// root. Cancelling ctx SIGKILLs the subprocess and Run returns ctx.Err().
// Independently, an internal timeout (timeout_ms, default 120s, capped at
// 600s) fires a [Result] with IsError set rather than an error return.
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
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()

	// The external ctx being cancelled always aborts the call, even if the
	// internal timeout also expired at the same moment.
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	output, truncated := truncateBytes(buf.String(), defaultMaxOutputBytes)

	if runCtx.Err() != nil {
		// Internal timeout fired; parent ctx is still live (checked above).
		return Result{
			IsError:  true,
			Content:  fmt.Sprintf("%s\n… command timed out after %s", output, timeout),
			Metadata: Metadata{Truncated: truncated},
		}, nil
	}

	exitCode := 0
	switch {
	case cmd.ProcessState != nil:
		exitCode = cmd.ProcessState.ExitCode()
	case runErr != nil:
		exitCode = -1
	}

	content := output
	if exitCode != 0 {
		content = fmt.Sprintf("%s\n[exit %d]", output, exitCode)
	}

	return Result{
		Content:  content,
		IsError:  exitCode != 0,
		Metadata: Metadata{ExitCode: &exitCode, Truncated: truncated},
	}, nil
}
