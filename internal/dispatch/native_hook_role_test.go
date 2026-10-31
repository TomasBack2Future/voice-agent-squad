package dispatch

import "testing"

func TestNativeHookConfigurationAdmitsOnlyWorkerAndDeployerRoles(t *testing.T) {
	base := legacyHookConfig{Actor: "actor", Native: "native", Runtime: "claude", Role: "worker", Fence: true}
	for _, row := range []struct {
		name   string
		mutate func(*legacyHookConfig)
		actor  string
		native string
		ok     bool
	}{
		{"worker", func(*legacyHookConfig) {}, "actor", "native", true},
		{"deployer", func(c *legacyHookConfig) { c.Role = "deployer" }, "actor", "native", true},
		{"dispatcher", func(c *legacyHookConfig) { c.Role = "dispatcher" }, "actor", "native", false},
		{"reviewer", func(c *legacyHookConfig) { c.Role = "reviewer" }, "actor", "native", false},
		{"investigator", func(c *legacyHookConfig) { c.Role = "investigator" }, "actor", "native", false},
		{"probe", func(c *legacyHookConfig) { c.Role = "probe" }, "actor", "native", false},
		{"empty role", func(c *legacyHookConfig) { c.Role = "" }, "actor", "native", false},
		{"role case", func(c *legacyHookConfig) { c.Role = "Deployer" }, "actor", "native", false},
		{"deployer other actor", func(c *legacyHookConfig) { c.Role = "deployer" }, "other", "native", false},
		{"deployer other native", func(c *legacyHookConfig) { c.Role = "deployer" }, "actor", "other", false},
		{"deployer other runtime", func(c *legacyHookConfig) { c.Role, c.Runtime = "deployer", "codex" }, "actor", "native", false},
		{"deployer without fence", func(c *legacyHookConfig) { c.Role, c.Fence = "deployer", false }, "actor", "native", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			cfg := base
			row.mutate(&cfg)
			if got := cfg.matches(row.actor, row.native); got != row.ok {
				t.Fatalf("matches=%v want %v", got, row.ok)
			}
		})
	}
}

func TestDeployerBootstrapClaimsItsCanonicalItemBeforeTheEnvironment(t *testing.T) {
	// The release owner is a bound native like any other: before its first
	// claim only the canonical item may be claimed. The protected environment
	// claim follows once that claim is held.
	s := bootstrapFixture(t)
	if _, err := s.db.Exec(`UPDATE dispatch_reservations SET source_ref='release:studio-example'`); err != nil {
		t.Fatal(err)
	}
	env := NativeHookTool{Name: "Bash", Command: "squad claim ENV-002 --intent release"}
	if err := record(s, "worker", "new-native", env); err == nil {
		t.Fatal("environment claim admitted before the canonical item claim")
	}
	if err := record(s, "worker", "new-native", NativeHookTool{Name: "Bash", Command: "squad claim T --intent release"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','worker',2,2,'held',1)`); err != nil {
		t.Fatal(err)
	}
	if err := record(s, "worker", "new-native", env); err != nil {
		t.Fatalf("environment claim refused after the canonical claim: %v", err)
	}
	if err := record(s, "intruder", "new-native", env); err == nil {
		t.Fatal("another actor rode the deployer native")
	}
}
