package reviewer

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json skills/squad-pr-reviewer-core/SKILL.md policies/studio-pr-review-policy.md
var releaseFS embed.FS

type Document struct {
	Name    string
	Path    string
	SHA256  string
	Content []byte
}

type Bundle struct {
	Core   Document
	Policy Document
}

type manifest struct {
	SchemaVersion string `json:"schema_version"`
	Documents     []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"documents"`
}

func Load() (Bundle, error) {
	raw, err := releaseFS.ReadFile("manifest.json")
	if err != nil {
		return Bundle{}, fmt.Errorf("read reviewer manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Bundle{}, fmt.Errorf("parse reviewer manifest: %w", err)
	}
	if m.SchemaVersion != "squad.reviewer.release.v1" {
		return Bundle{}, fmt.Errorf("unsupported reviewer manifest %q", m.SchemaVersion)
	}

	docs := make(map[string]Document, len(m.Documents))
	for _, entry := range m.Documents {
		if _, exists := docs[entry.Name]; exists {
			return Bundle{}, fmt.Errorf("duplicate reviewer document %q", entry.Name)
		}
		content, err := releaseFS.ReadFile(entry.Path)
		if err != nil {
			return Bundle{}, fmt.Errorf("read reviewer document %q: %w", entry.Name, err)
		}
		sum := sha256.Sum256(content)
		actual := hex.EncodeToString(sum[:])
		if actual != entry.SHA256 {
			return Bundle{}, fmt.Errorf("reviewer document %q hash mismatch", entry.Name)
		}
		docs[entry.Name] = Document{
			Name: entry.Name, Path: entry.Path, SHA256: entry.SHA256, Content: content,
		}
	}

	core, coreOK := docs["squad-pr-reviewer-core"]
	policy, policyOK := docs["studio-pr-review-policy"]
	if !coreOK || !policyOK || len(docs) != 2 {
		return Bundle{}, fmt.Errorf("reviewer manifest must contain exactly the core and Studio policy")
	}
	return Bundle{Core: core, Policy: policy}, nil
}
