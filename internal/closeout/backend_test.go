package closeout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommentUpsertUsesMarkerAndActualGitHubAuthor(t *testing.T) {
	for _, author := range []string{"", "operator", "other"} {
		t.Run("existing-"+author, func(t *testing.T) {
			writes := 0
			method := ""
			marker := "<!-- squad-closeout:D/1/final -->"
			b := LocalBackend{GitHubRunner: func(_ context.Context, input string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--jq .login") {
					return []byte("operator\n"), nil
				}
				if strings.Contains(joined, "--slurp") {
					pages := [][]map[string]any{{}}
					if author != "" {
						pages = [][]map[string]any{{{"id": 123, "body": marker + "\nold", "user": map[string]string{"login": author}}}}
					}
					raw, _ := json.Marshal(pages)
					return raw, nil
				}
				writes++
				if strings.Contains(joined, "PATCH") {
					method = "PATCH"
				} else {
					method = "POST"
				}
				if !strings.Contains(input, "new evidence") {
					t.Fatal("lost body")
				}
				return []byte(`{}`), nil
			}}
			err := b.Comment(context.Background(), "github:o/r#1", marker, "new evidence")
			if author == "other" {
				if err == nil || writes != 0 {
					t.Fatal("edited another author's comment")
				}
				return
			}
			if err != nil || writes != 1 {
				t.Fatalf("upsert %d %v", writes, err)
			}
			want := "POST"
			if author == "operator" {
				want = "PATCH"
			}
			if method != want {
				t.Fatalf("method %s want %s", method, want)
			}
		})
	}
}
func TestCanonicalSourceRejectsFlagsAndNonGitHubURLs(t *testing.T) {
	for _, s := range []string{"github:--repo/x#1", "https://example.com/r/1", "github:o/r#1 --admin"} {
		if _, _, err := ParseSource(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
