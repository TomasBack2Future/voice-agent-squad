package grokreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDisclosureReadbackDoesNotTransferPRGrantOrEraseDenial(t *testing.T) {
	owner := ReviewOwner{Actor: "original-worker", Native: "original-native"}
	for _, pair := range [][2]int{{91, 92}, {1245, 1275}} {
		request := ReviewDisclosure{SchemaVersion: ReviewDisclosureSchema, Reference: "human:retained", Disposition: "granted", Operation: "managed_review", Provider: "grok", Content: "source_diff_and_review_contract", Owner: owner, Identity: ReviewIdentity{Repository: "owner/repo", PR: pair[0], BaseRef: "main", BaseSHA: "base", HeadSHA: "head"}, Mode: "required"}
		write := func(name string, value ReviewDisclosure) string {
			raw, _ := json.Marshal(value)
			p := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(p, raw, 0600); err != nil {
				t.Fatal(err)
			}
			return p
		}
		for _, disposition := range []string{"granted", "pending", "denied"} {
			receipt := request
			receipt.Disposition = disposition
			path := write("receipt", receipt)
			scope := write("request", request)
			result, err := ReadDisclosure(path, scope, owner)
			if err != nil || result.Status != disposition || result.Sampled || result.Published {
				t.Fatalf("scope readback: %#v %v", result, err)
			}
			foreign := request
			foreign.Identity.PR = pair[1]
			result, err = ReadDisclosure(path, write("foreign", foreign), owner)
			if err != nil || result.Status != "scope-mismatch" || result.Reference != request.Reference || result.ReceiptDisposition != disposition {
				t.Fatalf("old grant/denial transferred: %#v %v", result, err)
			}
		}
		if _, err := ReadDisclosure("", write("request", request), ReviewOwner{Actor: "foreign", Native: owner.Native}); err == nil {
			t.Fatal("foreign recipient qualified")
		}
		result, err := ReadDisclosure("", write("request", request), owner)
		if err != nil || result.Status != "unavailable" {
			t.Fatal("missing permission invented")
		}
	}
}
