package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpListsCommandGroups(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--help"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("help failed: %v", err)
	}

	help := out.String()
	for _, want := range []string{"Build:", "Development:", "Release & Distribution:"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing group %q\n%s", want, help)
		}
	}
	for _, want := range []string{"build", "dev", "release", "ota", "upload"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing command %q\n%s", want, help)
		}
	}
}

func TestVersionDefaultsToDev(t *testing.T) {
	if Version != "dev" {
		t.Errorf("Version = %q, want dev (ldflags inject real versions)", Version)
	}
}
