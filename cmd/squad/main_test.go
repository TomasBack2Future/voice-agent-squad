package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Even commands such as version run post-command hygiene. Keep the default
// test process away from the operator's live ledger; individual tests can
// still replace SQUAD_HOME with their own fixtures through t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "squad-cli-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("SQUAD_HOME", home); err != nil {
		_ = os.RemoveAll(home)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func TestVersionString(t *testing.T) {
	if versionString == "" {
		t.Fatal("versionString must not be empty")
	}
	semver := regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	if !semver.MatchString(versionString) {
		t.Fatalf("versionString %q is not a valid semver", versionString)
	}
}

func TestVersionCommand_PrintsVersion(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != versionString {
		t.Fatalf("got %q want %q", got, versionString)
	}
}
