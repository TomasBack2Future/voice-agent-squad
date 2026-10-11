package main

import (
	"bytes"
	"testing"
)

func TestLegacyStopDecoderRejectsReceiptImportsAndAmbiguousInput(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"stopped":true}`), []byte(`{"processes":[]}`), []byte(`{"handoff":{"impersonate":true}}`), []byte(`{"inventory":[{"kind":"remote","joined":true}]}`), []byte(`{} {}`), bytes.Repeat([]byte(" "), 65537)} {
		if _, err := decodeLegacyStopInput(raw); err == nil {
			t.Fatal("untrusted native/host proof accepted")
		}
	}
}

func TestLegacyRecoveryDecoderRejectsImportedStopAndUnknownScope(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"stopped":true}`), []byte(`{"processes":[]}`), []byte(`{"handoff":{"imported_authority":true}}`), []byte(`{} {}`), bytes.Repeat([]byte(" "), 16385)} {
		if _, err := decodeLegacyRecoveryInput(raw); err == nil {
			t.Fatal("untrusted recovery/host proof accepted")
		}
	}
}
