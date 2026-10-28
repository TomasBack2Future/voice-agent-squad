package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

// Keep command tests away from the operator's live ledger.
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

func TestVersionJSONDoesNotInitializeCoordinationState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "untouched-home")
	t.Setenv("SQUAD_HOME", home)
	t.Setenv("SQUAD_NO_HYGIENE", "")
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Version   string `json:"version"`
		GoVersion string `json:"go_version"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != versionString || result.GoVersion == "" {
		t.Fatalf("missing build identity: %s", out.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("version changed coordination state: %v", err)
	}
}

func TestBuildVersionPreservesUnknownAndDirtyProvenance(t *testing.T) {
	if result := buildVersion(nil); result.Revision != "" || result.Modified != nil {
		t.Fatalf("missing provenance became known: %+v", result)
	}
	for _, dirty := range []string{"true", "false"} {
		result := buildVersion(&debug.BuildInfo{GoVersion: "go-test", Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "exact-source"}, {Key: "vcs.modified", Value: dirty},
		}})
		if result.Revision != "exact-source" || result.Modified == nil || *result.Modified != (dirty == "true") {
			t.Fatalf("incorrect provenance: %+v", result)
		}
	}
}
