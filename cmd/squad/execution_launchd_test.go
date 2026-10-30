package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func launchdFixture(t *testing.T) executionLaunchdOptions {
	t.Helper()
	root := t.TempDir()
	mk := func(name string, mode os.FileMode, dir bool) string {
		p := filepath.Join(root, name)
		if dir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			return p
		}
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return executionLaunchdOptions{
		Label: "com.example.admission", SquadBinary: mk("squad", 0o755, false), TokenFile: mk("admission.token", 0o600, false),
		Listen: "127.0.0.1:7788", WorkingDir: mk("ledger", 0, true), SquadHome: mk("home-squad", 0, true), Home: mk("home", 0, true),
		GH: mk("gh", 0o755, false), GHConfigDir: mk("ghcfg", 0, true), Agent: "admission-service", LogDir: mk("logs", 0, true),
	}
}

func TestLaunchdPlistPinsTheEnvironmentGhNeeds(t *testing.T) {
	o := launchdFixture(t)
	out, err := renderExecutionPlist(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<key>HOME</key>\n<string>" + o.Home + "</string>",
		"<key>GH_CONFIG_DIR</key>\n<string>" + o.GHConfigDir + "</string>",
		"<key>PATH</key>\n<string>" + filepath.Dir(o.GH) + ":/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<key>WorkingDirectory</key>\n<string>" + o.WorkingDir + "</string>",
		"<key>SuccessfulExit</key>\n<false/>",
		"<string>--token-file</string>\n<string>" + o.TokenFile + "</string>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	// The token's contents are never read, let alone embedded.
	if err := os.WriteFile(o.TokenFile, []byte(strings.Repeat("S", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	again, _ := renderExecutionPlist(o)
	if strings.Contains(again, "SSSS") {
		t.Fatal("token content rendered")
	}
}

func TestLaunchdPlistRefusesAnUnderspecifiedEnvironment(t *testing.T) {
	for name, mutate := range map[string]func(*executionLaunchdOptions){
		"relative gh":           func(o *executionLaunchdOptions) { o.GH = "gh" },
		"missing gh":            func(o *executionLaunchdOptions) { o.GH = filepath.Join(o.Home, "nope") },
		"missing gh config dir": func(o *executionLaunchdOptions) { o.GHConfigDir = filepath.Join(o.Home, "nope") },
		"relative home":         func(o *executionLaunchdOptions) { o.Home = "home" },
		"readable token":        func(o *executionLaunchdOptions) { _ = os.Chmod(o.TokenFile, 0o644) },
		"relative binary":       func(o *executionLaunchdOptions) { o.SquadBinary = "squad" },
		"missing log dir":       func(o *executionLaunchdOptions) { o.LogDir = filepath.Join(o.Home, "nope") },
		"label with slash":      func(o *executionLaunchdOptions) { o.Label = "a/b" },
	} {
		t.Run(name, func(t *testing.T) {
			o := launchdFixture(t)
			mutate(&o)
			if _, err := renderExecutionPlist(o); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestLaunchdPlistEscapesValues(t *testing.T) {
	o := launchdFixture(t)
	o.Agent = "agent&<x>"
	out, err := renderExecutionPlist(o)
	if err != nil || strings.Contains(out, "agent&<x>") || !strings.Contains(out, "agent&amp;&lt;x&gt;") {
		t.Fatalf("not escaped: %v\n%s", err, out)
	}
}
