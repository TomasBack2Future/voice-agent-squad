package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestMaintainedDocumentationLinks(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	link := regexp.MustCompile(`\[[^\]]+\]\(([^)[:space:]]+)\)`)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "docs/README.md", "docs/architecture.md", "docs/environments-and-ci.md", "docs/proposals/grok-required-review-gate.md", "workspace/README.md"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range link.FindAllStringSubmatch(string(data), -1) {
				target := strings.Trim(match[1], "<>")
				if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				target = strings.SplitN(target, "#", 2)[0]
				if _, err := os.Stat(filepath.Join(root, filepath.Dir(name), target)); err != nil {
					t.Errorf("broken local link %q: %v", match[1], err)
				}
			}
		})
	}
}

func TestReviewReferenceDoesNotDefineConsumerRoles(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	for _, name := range []string{"AGENTS.md", "docs/proposals/grok-required-review-gate.md", "docs/concepts/claims-and-coordination.md", "docs/reference/commands.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"Codex Worker", "The Dispatcher", "TomasBack2Future/voice-agent-studio", "DISPATCH-STUDIO-"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s contains consumer-specific policy %q", name, forbidden)
			}
		}
	}
}
