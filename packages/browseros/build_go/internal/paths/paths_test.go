package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRootFromWalksUpToBrowserosPyproject(t *testing.T) {
	tmp := t.TempDir()
	pkgRoot := filepath.Join(tmp, "repo", "packages", "browseros")
	writeFile(t, filepath.Join(pkgRoot, "pyproject.toml"), "[project]\nname = \"browseros\"\n")
	nested := filepath.Join(pkgRoot, "build", "config", "gn")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := RootFrom(nested)
	if err != nil {
		t.Fatalf("RootFrom: %v", err)
	}
	want, _ := filepath.EvalSymlinks(pkgRoot)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Errorf("RootFrom = %s, want %s", gotResolved, want)
	}
}

func TestRootFromIgnoresOtherPyprojects(t *testing.T) {
	tmp := t.TempDir()
	pkgRoot := filepath.Join(tmp, "repo", "packages", "browseros")
	writeFile(t, filepath.Join(tmp, "repo", "pyproject.toml"), "[project]\nname = \"otherproject\"\n")
	writeFile(t, filepath.Join(pkgRoot, "pyproject.toml"), "[project]\nname = 'browseros'\n")

	got, err := RootFrom(filepath.Join(pkgRoot, "build"))
	if err != nil {
		t.Fatalf("RootFrom: %v", err)
	}
	if filepath.Base(got) != "browseros" {
		t.Errorf("RootFrom = %s, want the browseros package dir", got)
	}
}

func TestRootFromErrorsWhenNoMarker(t *testing.T) {
	tmp := t.TempDir()
	_, err := RootFrom(tmp)
	if err == nil {
		t.Fatal("expected error outside a browseros checkout")
	}
	if !strings.Contains(err.Error(), "BROWSEROS_ROOT") {
		t.Errorf("error should mention the BROWSEROS_ROOT escape hatch: %v", err)
	}
}

func TestRootHonorsBrowserosRootOverride(t *testing.T) {
	tmp := t.TempDir()
	pkgRoot := filepath.Join(tmp, "browseros")
	writeFile(t, filepath.Join(pkgRoot, "pyproject.toml"), "name = \"browseros\"\n")

	t.Setenv("BROWSEROS_ROOT", pkgRoot)
	got, err := Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != pkgRoot {
		t.Errorf("Root = %s, want %s", got, pkgRoot)
	}
}

func TestRootRejectsInvalidOverride(t *testing.T) {
	t.Setenv("BROWSEROS_ROOT", t.TempDir())
	if _, err := Root(); err == nil {
		t.Fatal("expected error for BROWSEROS_ROOT without the pyproject marker")
	}
}

func TestRootFindsRealRepoFromPackageDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := RootFrom(cwd)
	if err != nil {
		t.Fatalf("RootFrom(%s): %v", cwd, err)
	}
	if filepath.Base(got) != "browseros" {
		t.Errorf("expected the real packages/browseros root, got %s", got)
	}
}
