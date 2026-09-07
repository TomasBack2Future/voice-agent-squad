package reviewer

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestLoadBundleVerifiesManifestAndRequiredDocuments(t *testing.T) {
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Core.Name != "squad-pr-reviewer-core" {
		t.Fatalf("core name = %q", bundle.Core.Name)
	}
	if bundle.Policy.Name != "studio-pr-review-policy" {
		t.Fatalf("policy name = %q", bundle.Policy.Name)
	}
	for _, doc := range []Document{bundle.Core, bundle.Policy} {
		sum := sha256.Sum256(doc.Content)
		if got := hex.EncodeToString(sum[:]); got != doc.SHA256 {
			t.Fatalf("%s hash = %s, manifest = %s", doc.Name, got, doc.SHA256)
		}
	}
}
