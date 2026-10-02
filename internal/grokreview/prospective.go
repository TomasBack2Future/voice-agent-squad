package grokreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	SchemaVersion         string                `json:"schema_version"`
	Original              LegacyRecoveryReceipt `json:"original"`
	DiffSHA256            string                `json:"diff_sha256"`
	BodySHA256            string                `json:"body_sha256"`
	ContentEvidencePath   string                `json:"content_evidence_path"`
	ContentEvidenceSHA256 string                `json:"content_evidence_sha256"`
	Owner                 ReviewOwner           `json:"owner"`
	Disclosure            ReviewDisclosure      `json:"disclosure"`
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
	if len(receipt.DiffSHA256) != 64 || len(receipt.BodySHA256) != 64 {
		return fmt.Errorf("original content identity unavailable")
	}
	var content map[string]json.RawMessage
	raw, err := readBoundedJSON(receipt.ContentEvidencePath, &content)
	if err != nil || receiptHash(raw) != receipt.ContentEvidenceSHA256 {
		return fmt.Errorf("original retained content evidence missing or changed")
	}
	var diffHash, bodyHash string
	if json.Unmarshal(content["diff_sha256"], &diffHash) != nil || json.Unmarshal(content["pr_body_sha256"], &bodyHash) != nil || diffHash != receipt.DiffSHA256 || bodyHash != receipt.BodySHA256 {
		return fmt.Errorf("original content hashes do not match retained evidence")
	}
	if err = a.verifyLegacyReceipt(original, false, verify); err != nil {
		return err
	}
	a.prospective = &receipt
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
	if tupleKey(identity) != tupleKey(p.Original.Attempt.Identity) || frozen.SchemaVersion != FrozenReviewSchemaVersion || receiptHash([]byte(frozen.Diff)) != p.DiffSHA256 || receiptHash([]byte(frozen.Description)) != p.BodySHA256 {
		return fmt.Errorf("prospective current tuple/content changed")
	}
	return nil
}
