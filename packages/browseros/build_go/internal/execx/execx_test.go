package execx

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func shellCmd(script string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", script}
	}
	return []string{"sh", "-c", script}
}

func TestRunCapturesSeparateStreamsWithoutStreaming(t *testing.T) {
	res, err := Default().Run(Cmd{Args: shellCmd("echo out; echo err 1>&2")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "out" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) != "err" {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if res.Code != 0 {
		t.Errorf("code = %d", res.Code)
	}
}

func TestRunReturnsExitCodeWithoutError(t *testing.T) {
	res, err := Default().Run(Cmd{Args: shellCmd("exit 3")})
	if err != nil {
		t.Fatalf("non-zero exit should not error: %v", err)
	}
	if res.Code != 3 {
		t.Errorf("code = %d, want 3", res.Code)
	}
}

func TestRunStreamingMergesAndCaptures(t *testing.T) {
	var stream bytes.Buffer
	res, err := Default().Run(Cmd{
		Args:   shellCmd("echo one; echo two 1>&2"),
		Stream: &stream,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	streamed := stream.String()
	if !strings.Contains(streamed, "one") || !strings.Contains(streamed, "two") {
		t.Errorf("stream missing merged output: %q", streamed)
	}
	if !strings.Contains(res.Stdout, "one") || !strings.Contains(res.Stdout, "two") {
		t.Errorf("captured stdout missing merged output: %q", res.Stdout)
	}
}

func TestRunRespectsDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	res, err := Default().Run(Cmd{
		Args: shellCmd("pwd; printf '%s' \"$EXECX_TEST_VAR\""),
		Dir:  dir,
		Env:  map[string]string{"EXECX_TEST_VAR": "hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("env var not passed: %q", res.Stdout)
	}
}

func TestCheckedFailsOnNonZeroExit(t *testing.T) {
	_, err := Checked(Default(), Cmd{Args: shellCmd("echo boom 1>&2; exit 1")})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry stderr detail: %v", err)
	}
}

func TestRecordingRunnerRecordsAndScripts(t *testing.T) {
	rec := &RecordingRunner{
		Results: []Result{{Stdout: "first"}, {Code: 1}},
		Errs:    []error{nil, nil, errors.New("third fails to start")},
	}

	res, err := rec.Run(Cmd{Args: []string{"git", "rev-parse", "HEAD"}, Dir: "/repo"})
	if err != nil || res.Stdout != "first" {
		t.Errorf("first call = (%+v, %v)", res, err)
	}
	res, err = rec.Run(Cmd{Args: []string{"git", "apply", "x.patch"}})
	if err != nil || res.Code != 1 {
		t.Errorf("second call = (%+v, %v)", res, err)
	}
	if _, err = rec.Run(Cmd{Args: []string{"gn", "gen"}}); err == nil {
		t.Error("third call should surface scripted error")
	}

	want := []string{"git rev-parse HEAD", "git apply x.patch", "gn gen"}
	got := rec.Argv()
	if len(got) != len(want) {
		t.Fatalf("argv = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if rec.Cmds[0].Dir != "/repo" {
		t.Errorf("dir not recorded: %+v", rec.Cmds[0])
	}
}
