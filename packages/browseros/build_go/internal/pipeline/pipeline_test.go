package pipeline

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

type fakeModule struct {
	name        string
	validateErr error
	executeErr  error
	calls       *[]string
}

func (m fakeModule) Name() string        { return m.name }
func (m fakeModule) Description() string { return "fake " + m.name }
func (m fakeModule) Validate(*buildctx.Context) error {
	*m.calls = append(*m.calls, "validate:"+m.name)
	return m.validateErr
}
func (m fakeModule) Execute(*buildctx.Context) error {
	*m.calls = append(*m.calls, "execute:"+m.name)
	return m.executeErr
}

func testCtx(t *testing.T) *buildctx.Context {
	t.Helper()
	plat := platform.Platform{OS: "macos", Arch: "arm64"}
	root := t.TempDir()
	ctx, err := buildctx.New(buildctx.Options{ChromiumSrc: "/x", Platform: &plat, RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func registryOf(calls *[]string, mods ...fakeModule) Registry {
	reg := Registry{}
	for _, m := range mods {
		m := m
		m.calls = calls
		reg[m.name] = func() Module { return m }
	}
	return reg
}

func TestExecuteRunsModulesInOrder(t *testing.T) {
	var calls []string
	reg := registryOf(&calls, fakeModule{name: "a"}, fakeModule{name: "b"}, fakeModule{name: "c"})

	if err := Execute(testCtx(t), []string{"c", "a", "b"}, reg, "build"); err != nil {
		t.Fatal(err)
	}
	want := []string{"validate:c", "execute:c", "validate:a", "execute:a", "validate:b", "execute:b"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestExecuteFailsFastOnValidationError(t *testing.T) {
	var calls []string
	reg := registryOf(&calls,
		fakeModule{name: "ok"},
		fakeModule{name: "bad", validateErr: errors.New("missing tool")},
		fakeModule{name: "never"},
	)

	err := Execute(testCtx(t), []string{"ok", "bad", "never"}, reg, "build")
	if err == nil || !strings.Contains(err.Error(), "validation failed for bad") {
		t.Fatalf("err = %v", err)
	}
	joined := strings.Join(calls, ",")
	if strings.Contains(joined, "execute:bad") {
		t.Error("bad module must not execute after failed validation")
	}
	if strings.Contains(joined, "validate:never") {
		t.Error("pipeline must stop at first failure")
	}
}

func TestExecuteStopsOnModuleError(t *testing.T) {
	var calls []string
	reg := registryOf(&calls,
		fakeModule{name: "boom", executeErr: errors.New("kaput")},
		fakeModule{name: "after"},
	)
	err := Execute(testCtx(t), []string{"boom", "after"}, reg, "build")
	if err == nil || !strings.Contains(err.Error(), "module boom failed") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.Join(calls, ","), "validate:after") {
		t.Error("pipeline must not continue after module failure")
	}
}

func TestValidateListsAllInvalidModules(t *testing.T) {
	var calls []string
	reg := registryOf(&calls, fakeModule{name: "clean"})
	err := Validate([]string{"clean", "nope1", "nope2"}, reg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nope1") || !strings.Contains(err.Error(), "nope2") {
		t.Errorf("error should list every invalid module: %v", err)
	}
	if err := Validate([]string{"clean"}, reg); err != nil {
		t.Errorf("valid pipeline should pass: %v", err)
	}
}

// TestNotificationsFireOnlyForKeyModulesAndSwallowFailures covers the Slack
// gating: only NOTIFY_MODULES get module events, pipeline start/end always
// fire, and a failing webhook never breaks the build.
func TestNotificationsFireOnlyForKeyModulesAndSwallowFailures(t *testing.T) {
	var mu sync.Mutex
	var events []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Attachments []struct {
				Text string `json:"text"`
			} `json:"attachments"`
		}
		json.Unmarshal(body, &payload)
		mu.Lock()
		if len(payload.Attachments) > 0 {
			events = append(events, payload.Attachments[0].Text)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError) // failures must be swallowed
	}))
	defer server.Close()
	t.Setenv("SLACK_WEBHOOK_URL", server.URL)

	var calls []string
	reg := registryOf(&calls, fakeModule{name: "clean"}, fakeModule{name: "compile"})
	if err := Execute(testCtx(t), []string{"clean", "compile"}, reg, "build"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected 4 notifications, got %d", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	all := strings.Join(events, "\n")
	mu.Unlock()
	for _, want := range []string{"Pipeline Started", "Module 'compile' started", "Module 'compile' completed", "Pipeline Completed"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing notification %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Module 'clean'") {
		t.Errorf("clean is not a NOTIFY_MODULE, should not notify:\n%s", all)
	}
}
