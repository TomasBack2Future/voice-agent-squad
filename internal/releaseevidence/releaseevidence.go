// Package releaseevidence evaluates the evidence a release owner presents for a
// production release item. It maps real release facts (candidate identity,
// execution result, acceptance, observation, cleanup, custody) onto the
// existing attestation ledger; it never infers completion from a role or from
// a process exit, and it creates no second state ledger.
package releaseevidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	Schema   = "squad.release-evidence.v1"
	MaxBytes = 64 * 1024

	StateComplete        = "complete"
	StatePartial         = "partial"
	StateBlocked         = "blocked"
	StateCustodyRetained = "custody-retained"
	StateInvalid         = "invalid"
)

// Check is one acceptance or observation result with a pointer to its evidence.
type Check struct {
	Name     string `json:"name"`
	Required *bool  `json:"required,omitempty"` // default true
	Result   string `json:"result"`             // passed|failed|pending|not-applicable
	Ref      string `json:"ref"`
}

// Component is one deployed image of the candidate. StagingRollout is the
// actual staging outcome for that exact digest: verified, rolled-back,
// not-deployed or failed. A rebuild of the same SHA has a new digest and is a
// new candidate; it never inherits the earlier build's staging evidence.
type Component struct {
	Name           string `json:"name"`
	Digest         string `json:"digest"`
	StagingDigest  string `json:"staging_digest"`
	StagingRollout string `json:"staging_rollout"`
	Ref            string `json:"ref"`
}

var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// componentGaps reports why a component is not proven by staging. A malformed
// entry is invalid; a mismatch or an unverified rollout keeps the release open.
func componentGaps(cs []Component) (invalid, partial []string) {
	seen := map[string]bool{}
	for _, c := range cs {
		switch {
		case !refPattern.MatchString(c.Name) || seen[c.Name]:
			invalid = append(invalid, "candidate.components: each component needs a unique simple name")
			continue
		case !imageDigestPattern.MatchString(c.Digest) || !imageDigestPattern.MatchString(c.StagingDigest):
			invalid = append(invalid, "candidate.components."+c.Name+": digest and staging_digest must be sha256:<64 hex>")
			continue
		}
		seen[c.Name] = true
		if c.Digest != c.StagingDigest {
			partial = append(partial, "component "+c.Name+": production digest differs from the staging-verified digest (a rebuild is a new candidate and needs its own staging verification)")
		}
		switch c.StagingRollout {
		case "verified":
		case "rolled-back", "not-deployed", "failed":
			partial = append(partial, "component "+c.Name+": staging rollout "+c.StagingRollout+"; the candidate has not passed staging for this component")
		default:
			invalid = append(invalid, "candidate.components."+c.Name+": staging_rollout must be verified, rolled-back, not-deployed or failed")
		}
		needRef(&partial, "candidate.components."+c.Name+".ref", c.Ref)
	}
	return invalid, partial
}

type Document struct {
	Schema     string `json:"schema"`
	ReleaseRef string `json:"release_ref"`
	Candidate  struct {
		ApplicationSHA string `json:"application_sha"`
		Version        string `json:"version"`
		ManifestSHA256 string `json:"manifest_sha256"`
		// Components, when present, bind the released images to staging: each
		// production digest must be the digest staging actually verified.
		Components []Component `json:"components,omitempty"`
	} `json:"candidate"`
	Execution struct {
		Result string `json:"result"` // succeeded|failed|blocked|pending
		Ref    string `json:"ref"`
	} `json:"execution"`
	Acceptance  []Check `json:"acceptance"`
	Observation Check   `json:"observation"`
	Cleanup     struct {
		Result string `json:"result"` // complete|failed|pending
		Ref    string `json:"ref"`
	} `json:"cleanup"`
	Custody struct {
		Disposition string   `json:"disposition"` // none|released|retained
		Retained    []string `json:"retained,omitempty"`
		Ref         string   `json:"ref,omitempty"`
	} `json:"custody"`
	// Current points at the authoritative current conclusion that reports and
	// checkpoints should cite; frozen candidate material stays immutable.
	Current string `json:"current_conclusion_ref,omitempty"`
}

// Verdict is the deterministic evaluation of a document. Unmet lists each
// unsatisfied condition so an owner can resume from a recorded position.
type Verdict struct {
	Schema     string   `json:"schema"`
	State      string   `json:"state"`
	Satisfied  bool     `json:"satisfied"`
	ReleaseRef string   `json:"release_ref"`
	Candidate  string   `json:"candidate,omitempty"`
	Unmet      []string `json:"unmet"`
}

var (
	refPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)
	titleRefPattern = regexp.MustCompile(`^\[release:([A-Za-z0-9][A-Za-z0-9_.-]{0,95})\]`)
	shaPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// RefFromTitle returns the release ref of an item titled "[release:<ref>] ...".
func RefFromTitle(title string) (string, bool) {
	m := titleRefPattern.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// RefFromSource returns the ref of a reservation source "release:<ref>".
func RefFromSource(source string) (string, bool) {
	ref, ok := strings.CutPrefix(source, "release:")
	return ref, ok && refPattern.MatchString(ref)
}

// Parse decodes a bounded document, rejecting unknown fields.
func Parse(b []byte) (Document, error) {
	var d Document
	if len(b) == 0 || len(b) > MaxBytes {
		return d, fmt.Errorf("release evidence must be 1..%d bytes", MaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("invalid release evidence: %w", err)
	}
	return d, nil
}

func needRef(unmet *[]string, what, v string) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 300 || strings.ContainsAny(v, "\r\n") {
		*unmet = append(*unmet, what+": bounded single-line evidence reference required")
	}
}

func required(c Check) bool { return c.Required == nil || *c.Required }

// Evaluate decides whether the document proves the release for wantRef. Only
// StateComplete satisfies the gate. Precedence: invalid, blocked (a recorded
// failure), custody-retained, partial.
func Evaluate(d Document, wantRef string) Verdict {
	v := Verdict{Schema: Schema, ReleaseRef: d.ReleaseRef, Unmet: []string{}}
	var invalid, failed, partial []string
	if d.Schema != Schema {
		invalid = append(invalid, "schema must be "+Schema)
	}
	if d.ReleaseRef != wantRef || wantRef == "" {
		invalid = append(invalid, "release_ref must equal the item's release ref "+wantRef)
	}
	ci, cp := componentGaps(d.Candidate.Components)
	invalid = append(invalid, ci...)
	partial = append(partial, cp...)
	if !shaPattern.MatchString(d.Candidate.ApplicationSHA) {
		invalid = append(invalid, "candidate.application_sha must be an exact 40-hex SHA")
	} else {
		v.Candidate = d.Candidate.ApplicationSHA
	}
	if !sha256Pattern.MatchString(d.Candidate.ManifestSHA256) {
		invalid = append(invalid, "candidate.manifest_sha256 must be a 64-hex digest")
	}
	if strings.TrimSpace(d.Candidate.Version) == "" || len(d.Candidate.Version) > 64 {
		invalid = append(invalid, "candidate.version required")
	}

	switch d.Execution.Result {
	case "succeeded":
		needRef(&partial, "execution.ref", d.Execution.Ref)
	case "failed", "blocked":
		failed = append(failed, "execution "+d.Execution.Result)
	case "pending":
		partial = append(partial, "execution not finished")
	default:
		invalid = append(invalid, "execution.result must be succeeded|failed|blocked|pending")
	}

	if len(d.Acceptance) == 0 {
		partial = append(partial, "acceptance: at least one check required")
	}
	if len(d.Acceptance) > 100 {
		invalid = append(invalid, "acceptance: more than 100 checks")
	}
	check := func(prefix string, c Check) {
		name := prefix + c.Name
		if strings.TrimSpace(c.Name) == "" && prefix == "acceptance " {
			invalid = append(invalid, "acceptance check name required")
		}
		switch c.Result {
		case "passed":
			needRef(&partial, name+" ref", c.Ref)
		case "not-applicable":
			if required(c) && strings.TrimSpace(c.Ref) == "" {
				partial = append(partial, name+": not-applicable needs a justification reference")
			}
		case "failed":
			if required(c) {
				failed = append(failed, name+" failed")
			}
		case "pending", "":
			if required(c) {
				partial = append(partial, name+" not finished")
			}
		default:
			invalid = append(invalid, name+": result must be passed|failed|pending|not-applicable")
		}
	}
	for _, c := range d.Acceptance {
		check("acceptance ", c)
	}
	d.Observation.Name = "observation"
	check("", d.Observation)

	switch d.Cleanup.Result {
	case "complete":
		needRef(&partial, "cleanup.ref", d.Cleanup.Ref)
	case "failed":
		failed = append(failed, "cleanup failed")
	case "pending", "":
		partial = append(partial, "cleanup not finished")
	default:
		invalid = append(invalid, "cleanup.result must be complete|failed|pending")
	}

	retained := false
	switch d.Custody.Disposition {
	case "none", "released":
		if len(d.Custody.Retained) > 0 {
			invalid = append(invalid, "custody.retained must be empty unless disposition is retained")
		}
		if d.Custody.Disposition == "released" {
			needRef(&partial, "custody.ref", d.Custody.Ref)
		}
	case "retained":
		retained = true
		if len(d.Custody.Retained) == 0 {
			invalid = append(invalid, "custody.retained must name the retained resources")
		}
	default:
		invalid = append(invalid, "custody.disposition must be none|released|retained")
	}
	if len(d.Custody.Retained) > 50 {
		invalid = append(invalid, "custody.retained exceeds 50 entries")
	}

	switch {
	case len(invalid) > 0:
		v.State, v.Unmet = StateInvalid, invalid
	case len(failed) > 0:
		v.State, v.Unmet = StateBlocked, failed
	case retained:
		v.State = StateCustodyRetained
		v.Unmet = append([]string{"custody retained: " + strings.Join(d.Custody.Retained, ", ")}, partial...)
	case len(partial) > 0:
		v.State, v.Unmet = StatePartial, partial
	default:
		v.State, v.Satisfied = StateComplete, true
	}
	return v
}

// Latest is the newest recorded release verdict for an item.
type Latest struct {
	AttestationID int64   `json:"attestation_id"`
	OutputHash    string  `json:"output_hash"`
	RecordedAt    int64   `json:"recorded_at"`
	CurrentRef    string  `json:"current_conclusion_ref,omitempty"`
	Verdict       Verdict `json:"verdict"`
}

// ReadLatest returns the newest non-revoked release attestation's recorded
// verdict, or nil. The verdict is re-derived from the hashed artifact, so a
// tampered or missing file never reads as satisfied.
func ReadLatest(ctx context.Context, db *sql.DB, repo, item string) (*Latest, error) {
	var l Latest
	var path string
	err := db.QueryRowContext(ctx, `SELECT id,output_hash,output_path,created_at FROM attestations WHERE repo_id=? AND item_id=? AND kind='release' AND NOT EXISTS (SELECT 1 FROM attestation_revocations v WHERE v.attestation_id=attestations.id) ORDER BY created_at DESC,id DESC LIMIT 1`, repo, item).Scan(&l.AttestationID, &l.OutputHash, &path, &l.RecordedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > MaxBytes {
		l.Verdict = Verdict{Schema: Schema, State: StateInvalid, Unmet: []string{"release evidence artifact unavailable"}}
		return &l, nil
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != l.OutputHash {
		l.Verdict = Verdict{Schema: Schema, State: StateInvalid, Unmet: []string{"release evidence artifact hash mismatch"}}
		return &l, nil
	}
	var rec Artifact
	if json.Unmarshal(b, &rec) != nil || rec.Verdict.Schema != Schema {
		l.Verdict = Verdict{Schema: Schema, State: StateInvalid, Unmet: []string{"release evidence artifact unreadable"}}
		return &l, nil
	}
	l.Verdict, l.CurrentRef = rec.Verdict, rec.Document.Current
	return &l, nil
}

// Artifact is the hashed attestation output: the submitted document and verdict.
type Artifact struct {
	Document Document `json:"document"`
	Verdict  Verdict  `json:"verdict"`
}
