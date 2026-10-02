package grokreview

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func backfillRecoveryRoots(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT parent,child,identity FROM review_recoveries")
	if err != nil {
		return err
	}
	type root struct{ parent, child, key string }
	var roots []root
	for rows.Next() {
		var parent, child, raw string
		if err := rows.Scan(&parent, &child, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var identity ReviewIdentity
		if err := json.Unmarshal([]byte(raw), &identity); err != nil {
			_ = rows.Close()
			return err
		}
		roots = append(roots, root{parent, child, tupleKey(identity)})
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, root := range roots {
		if _, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO review_recovery_roots(tuple,parent,child) VALUES(?,?,?)", root.key, root.parent, root.child); err != nil {
			return err
		}
	}
	return nil
}

// ProspectiveReadmission is a new-input authorization, never an original input
// reconstruction. Native custody and original content hashes stay independently
// evidenced; disclosure is scoped to this owner and exact review tuple.
type ProspectiveReadmission struct {
	RendererEquivalence   *PatchRendererEvidence `json:"renderer_equivalence,omitempty"`
	SchemaVersion         string                 `json:"schema_version"`
	Original              LegacyRecoveryReceipt  `json:"original"`
	DiffSHA256            string                 `json:"diff_sha256"`
	BodySHA256            string                 `json:"body_sha256"`
	ContentEvidencePath   string                 `json:"content_evidence_path"`
	ContentEvidenceSHA256 string                 `json:"content_evidence_sha256"`
	Owner                 ReviewOwner            `json:"owner"`
	Disclosure            ReviewDisclosure       `json:"disclosure"`
}

type ReviewOwner struct {
	Actor  string `json:"actor"`
	Native string `json:"native"`
}

type ReviewDisclosure struct {
	SchemaVersion string         `json:"schema_version"`
	Reference     string         `json:"reference"`
	Disposition   string         `json:"disposition"`
	Operation     string         `json:"operation"`
	Provider      string         `json:"provider"`
	Content       string         `json:"content"`
	Owner         ReviewOwner    `json:"owner"`
	Identity      ReviewIdentity `json:"identity"`
	Mode          string         `json:"mode"`
}

func (a *Admission) ImportProspective(path string, owner ReviewOwner) error {
	var receipt ProspectiveReadmission
	if _, err := readBoundedJSON(path, &receipt); err != nil {
		return err
	}
	return a.importProspective(receipt, owner, verifyNativeJoin)
}

func (a *Admission) importProspective(receipt ProspectiveReadmission, owner ReviewOwner, verify func(NativeJoinProof, AttemptReceipt) error) error {
	original := receipt.Original
	r := original.Attempt
	disclosure := receipt.Disclosure
	if receipt.SchemaVersion != "squad.review-readmission.prospective.v1" || original.InputReceiptPath != "" || original.InputReceiptSHA256 != "" || r.Identity.BundleSHA256 != "" || r.InputProvenance != "" {
		return fmt.Errorf("prospective admission requires explicitly unavailable original full input; never supply reconstructed legacy input")
	}
	if owner.Actor == "" || owner.Native == "" || receipt.Owner != owner || original.NativeJoin == nil || original.NativeJoin.NativeSession != owner.Native {
		return fmt.Errorf("prospective admission must belong to the original native delivery owner")
	}
	request := ReviewDisclosure{Owner: owner, Identity: r.Identity, Mode: r.Settings.Mode, Operation: "prospective_legacy_readmission", Provider: "grok", Content: "source_diff_and_review_contract"}
	if disclosure.Disposition != "granted" || !disclosureMatches(disclosure, request) {
		return fmt.Errorf("explicit current-tuple prospective disclosure authorization absent, denied or mismatched; preserve the pending user request")
	}
	if !contentHashValid(receipt.DiffSHA256) || !contentHashValid(receipt.BodySHA256) {
		return fmt.Errorf("original content identity unavailable")
	}
	var content retainedContent
	raw, err := readBoundedJSON(receipt.ContentEvidencePath, &content)
	if err != nil || receiptHash(raw) != receipt.ContentEvidenceSHA256 {
		return fmt.Errorf("original retained content evidence missing or changed")
	}
	diffHash, diffErr := content.hash("diff_sha256", "complete_diff_sha256")
	bodyHash, bodyErr := content.hash("pr_body_sha256", "body_sha256")
	if diffErr != nil || bodyErr != nil || diffHash != receipt.DiffSHA256 || bodyHash != receipt.BodySHA256 {
		return fmt.Errorf("original content hashes do not match retained evidence")
	}
	if _, diffAlias := content["complete_diff_sha256"]; diffAlias || content["body_sha256"] != nil {
		if err := content.verifyHistoricalAdmission(raw, r); err != nil {
			return err
		}
	}
	if receipt.RendererEquivalence != nil {
		if _, err := receipt.verifyRenderer(nil); err != nil {
			return err
		}
	}
	if err = a.verifyLegacyReceipt(original, false, verify); err != nil {
		return err
	}
	a.prospective = &receipt
	return nil
}

// Only the original admission's two maintained aliases are supported. Preserve
// the raw artifact: resolving an alias never reconstructs the old full input.
type retainedContent map[string]json.RawMessage

func (c *retainedContent) UnmarshalJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("content evidence must be one JSON object")
	}
	*c = make(retainedContent)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return fmt.Errorf("content evidence field is not a name")
		}
		if _, exists := (*c)[name]; exists {
			return fmt.Errorf("duplicate content evidence field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return err
		}
		(*c)[name] = value
	}
	_, err = d.Token()
	return err
}

func contentHashValid(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil
}

func (c retainedContent) hash(canonical, alias string) (string, error) {
	var resolved string
	for _, name := range []string{canonical, alias} {
		raw, exists := c[name]
		if !exists {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || !contentHashValid(value) || (resolved != "" && resolved != value) {
			return "", fmt.Errorf("invalid or conflicting content hash")
		}
		resolved = value
	}
	if resolved == "" {
		return "", fmt.Errorf("content hash missing")
	}
	return resolved, nil
}

func (c retainedContent) verifyHistoricalAdmission(raw []byte, original AttemptReceipt) error {
	// This is the unversioned historical admission layout, not arbitrary JSON
	// with two hashes. Unknown layouts need a separately maintained adapter.
	allowed := strings.Fields("repository pr base_sha head_sha complete_diff_sha256 body_sha256 diff_sha256 pr_body_sha256 remote_body_verified clean_worktree remote_head_verified stable_at_admission review_ready_at review_started_at review_process_session review_invocations_for_companion review_invocations_for_this_exact_tuple prior_failure verified_correction_admission superseded_inputs post_freeze_mutations_while_in_flight mode reasoning_effort timeout doctor fast_gates companion_audit known_independent_hold ci_run")
	for name := range c {
		known := false
		for _, field := range allowed {
			if name == field {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unsupported historical content evidence field")
		}
	}
	var admission struct {
		Repository string `json:"repository"`
		PR         int    `json:"pr"`
		BaseSHA    string `json:"base_sha"`
		HeadSHA    string `json:"head_sha"`
		Mode       string `json:"mode"`
	}
	if json.Unmarshal(raw, &admission) != nil || admission.Repository != original.Identity.Repository || admission.PR != original.Identity.PR || admission.BaseSHA != original.Identity.BaseSHA || admission.HeadSHA != original.Identity.HeadSHA || admission.Mode != original.Settings.Mode {
		return fmt.Errorf("historical content admission tuple or mode mismatched")
	}
	return nil
}

func tupleKey(identity ReviewIdentity) string {
	identity.BundleSHA256 = ""
	raw, _ := json.Marshal(identity)
	return string(raw)
}

func (a *Admission) verifyProspectiveBundle(bundle []byte, identity ReviewIdentity) error {
	p := a.prospective
	var frozen FrozenReviewBundle
	if err := json.Unmarshal(bundle, &frozen); err != nil {
		return err
	}
	if tupleKey(identity) != tupleKey(p.Original.Attempt.Identity) || frozen.SchemaVersion != FrozenReviewSchemaVersion || receiptHash([]byte(frozen.Description)) != p.BodySHA256 {
		return fmt.Errorf("prospective current tuple/content changed")
	}
	if p.RendererEquivalence != nil {
		_, err := p.verifyRenderer([]byte(frozen.Diff))
		return err
	}
	if receiptHash([]byte(frozen.Diff)) != p.DiffSHA256 {
		return fmt.Errorf("prospective current diff changed; complete renderer evidence required")
	}
	return nil
}
