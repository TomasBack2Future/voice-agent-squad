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
