package dispatch

import (
	"strings"
	"testing"
)

func TestHookObservationNamesTheBinaryThatServedTheCall(t *testing.T) {
	s := bootstrapFixture(t)
	s.binary = BinaryIdentity{Version: "0.0.0-squad.abc1234", Path: "/runtime/squad", SHA256: strings.Repeat("a", 64)}
	if err := record(s, "worker", "new-native", NativeHookTool{Name: "Read"}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT observation FROM worker_native_hook_observations WHERE native_session='new-native'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":"0.0.0-squad.abc1234"`, `"sha256":"` + strings.Repeat("a", 64) + `"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("observation lacks %s: %s", want, raw)
		}
	}
}
