// Package execx runs external commands. It ports build/common/utils.py
// run_command (real-time streaming with full capture) and adds a recording
// fake so modules can be tested as command-sequence assertions.
package execx

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// Cmd describes one external command invocation.
type Cmd struct {
	Args []string // argv; Args[0] is the executable
	Dir  string
	Env  map[string]string // extra vars merged over the process env
	// Stream, when set, receives merged stdout+stderr line by line as the
	// command runs (Python run_command behavior). When nil, stdout/stderr
	// are captured separately and nothing is printed.
	Stream io.Writer
	Stdin  string
}

// String renders the argv for logs and assertions.
func (c Cmd) String() string { return strings.Join(c.Args, " ") }

// Result holds the outcome of a command.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Runner executes commands. Run returns an error only when the command could
// not be started; a non-zero exit lands in Result.Code (callers that want
// fail-fast semantics use Checked).
type Runner interface {
	Run(c Cmd) (Result, error)
}

// realRunner shells out for real.
type realRunner struct{}

// Default returns the production Runner.
func Default() Runner { return realRunner{} }

func (realRunner) Run(c Cmd) (Result, error) {
	if len(c.Args) == 0 {
		return Result{}, fmt.Errorf("execx: empty command")
	}
	command := exec.Command(c.Args[0], c.Args[1:]...)
	command.Dir = c.Dir
	if len(c.Env) > 0 {
		command.Env = mergedEnv(c.Env)
	}
	if c.Stdin != "" {
		command.Stdin = strings.NewReader(c.Stdin)
	}

	if c.Stream == nil {
		var stdout, stderr strings.Builder
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		res := Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: exitCode(command, err)}
		if err != nil && !isExitError(err) {
			return res, err
		}
		return res, nil
	}

	// Streaming mode: merge stderr into stdout, emit line by line, capture all
	// (utils.py run_command).
	logx.Info(fmt.Sprintf("🔧 Running: %s", c))
	pipe, err := command.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	command.Stderr = command.Stdout // merge via the same pipe
	if err := command.Start(); err != nil {
		return Result{}, err
	}
	var lines []string
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}
		fmt.Fprintln(c.Stream, line)
		logx.ToFile("RUN_COMMAND: STDOUT: " + line)
		lines = append(lines, line)
	}
	waitErr := command.Wait()
	res := Result{Stdout: strings.Join(lines, "\n"), Code: exitCode(command, waitErr)}
	logx.ToFile(fmt.Sprintf("RUN_COMMAND: command completed with exit code: %d", res.Code))
	if waitErr != nil && !isExitError(waitErr) {
		return res, waitErr
	}
	return res, nil
}

func mergedEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func exitCode(command *exec.Cmd, err error) int {
	if command.ProcessState != nil {
		return command.ProcessState.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func isExitError(err error) bool {
	_, ok := err.(*exec.ExitError)
	return ok
}

// Checked runs c and converts a non-zero exit into an error naming the
// command (Python run_command check=True).
func Checked(r Runner, c Cmd) (Result, error) {
	res, err := r.Run(c)
	if err != nil {
		return res, fmt.Errorf("command failed to run: %s: %w", c, err)
	}
	if res.Code != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = lastLines(res.Stdout, 5)
		}
		if detail != "" {
			return res, fmt.Errorf("command failed (exit %d): %s\n%s", res.Code, c, detail)
		}
		return res, fmt.Errorf("command failed (exit %d): %s", res.Code, c)
	}
	return res, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// RecordingRunner captures every Cmd and replays scripted results; the
// zero value answers success with empty output to everything.
type RecordingRunner struct {
	Cmds []Cmd
	// Handler, when set, computes the result per command.
	Handler func(Cmd) (Result, error)
	// Scripted results consumed in order when Handler is nil.
	Results []Result
	Errs    []error
}

func (r *RecordingRunner) Run(c Cmd) (Result, error) {
	idx := len(r.Cmds)
	r.Cmds = append(r.Cmds, c)
	if r.Handler != nil {
		return r.Handler(c)
	}
	var res Result
	if idx < len(r.Results) {
		res = r.Results[idx]
	}
	var err error
	if idx < len(r.Errs) {
		err = r.Errs[idx]
	}
	return res, err
}

// Argv returns each recorded command as a joined string.
func (r *RecordingRunner) Argv() []string {
	out := make([]string, len(r.Cmds))
	for i, c := range r.Cmds {
		out[i] = c.String()
	}
	return out
}
