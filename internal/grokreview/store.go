package grokreview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrFenceLost = errors.New("review sampler fence lost")

type Verdict string

const (
	VerdictApproved Verdict = "approved"
	VerdictBlocking Verdict = "blocking"
	VerdictError    Verdict = "error"
)

type ReviewSeed struct {
	Key            string
	InstallationID int64
	RepositoryID   int64
	Repository     string
	PullRequest    int64
	BaseRef        string
	BaseSHA        string
	HeadSHA        string
	CoreHash       string
	PolicyHash     string
	Release        string
	ModelHash      string
}

type Lease struct {
	Key        string
	Owner      string
	Generation int64
}

type TerminalResult struct {
	Verdict      Verdict
	Summary      string
	FindingsJSON []byte
}

type Record struct {
	ReviewSeed
	State           string
	Owner           string
	LeaseGeneration int64
	LeaseUntil      time.Time
	ProviderStarted bool
	Verdict         Verdict
	Summary         string
	FindingsJSON    []byte
	SealedAt        time.Time
}

type AcquireState string

const (
	AcquireWon    AcquireState = "won"
	AcquireJoined AcquireState = "joined"
	AcquireSealed AcquireState = "sealed"
)

type AcquireResult struct {
	State  AcquireState
	Lease  Lease
	Record Record
}

type HeadKey struct {
	InstallationID int64
	RepositoryID   int64
	HeadSHA        string
	CheckName      string
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open reviewer store: %w", err)
	}
	db.SetMaxOpenConns(8)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS grok_review_records (
    review_key TEXT PRIMARY KEY,
    installation_id INTEGER NOT NULL,
    repository_id INTEGER NOT NULL,
    repository TEXT NOT NULL,
    pull_request INTEGER NOT NULL,
    base_ref TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    core_hash TEXT NOT NULL,
    policy_hash TEXT NOT NULL,
    adapter_release TEXT NOT NULL,
    model_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('in_progress', 'sealed')),
    lease_owner TEXT NOT NULL,
    lease_generation INTEGER NOT NULL,
    lease_until_ns INTEGER NOT NULL,
    provider_started INTEGER NOT NULL DEFAULT 0 CHECK (provider_started IN (0, 1)),
    verdict TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    findings_json BLOB,
    sealed_at_ns INTEGER NOT NULL DEFAULT 0,
    created_at_ns INTEGER NOT NULL,
    updated_at_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grok_review_head_generations (
    installation_id INTEGER NOT NULL,
    repository_id INTEGER NOT NULL,
    head_sha TEXT NOT NULL,
    check_name TEXT NOT NULL,
    generation INTEGER NOT NULL,
    PRIMARY KEY (installation_id, repository_id, head_sha, check_name)
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate reviewer store: %w", err)
	}
	return nil
}

func (s *Store) Acquire(ctx context.Context, seed ReviewSeed, owner string, now time.Time, ttl time.Duration) (AcquireResult, error) {
	if err := validateAcquire(seed, owner, ttl); err != nil {
		return AcquireResult{}, err
	}
	nowNS := now.UnixNano()
	leaseNS := now.Add(ttl).UnixNano()
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO grok_review_records (
    review_key, installation_id, repository_id, repository, pull_request,
    base_ref, base_sha, head_sha, core_hash, policy_hash, adapter_release,
    model_hash, state, lease_owner, lease_generation, lease_until_ns,
    created_at_ns, updated_at_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'in_progress', ?, 1, ?, ?, ?)`,
		seed.Key, seed.InstallationID, seed.RepositoryID, seed.Repository,
		seed.PullRequest, seed.BaseRef, seed.BaseSHA, seed.HeadSHA, seed.CoreHash,
		seed.PolicyHash, seed.Release, seed.ModelHash, owner, leaseNS, nowNS, nowNS)
	if err != nil {
		return AcquireResult{}, fmt.Errorf("create review record: %w", err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return AcquireResult{}, fmt.Errorf("read create result: %w", err)
	}
	if created == 1 {
		record, err := s.load(ctx, seed.Key)
		if err != nil {
			return AcquireResult{}, err
		}
		return won(record), nil
	}

	for attempts := 0; attempts < 4; attempts++ {
		record, err := s.load(ctx, seed.Key)
		if err != nil {
			return AcquireResult{}, err
		}
		if err := sameSeed(record.ReviewSeed, seed); err != nil {
			return AcquireResult{}, err
		}
		if record.State == "sealed" {
			return AcquireResult{State: AcquireSealed, Record: record}, nil
		}
		if record.LeaseUntil.After(now) {
			return AcquireResult{State: AcquireJoined, Record: record}, nil
		}

		if record.ProviderStarted {
			updated, err := s.db.ExecContext(ctx, `
UPDATE grok_review_records
SET state = 'sealed', verdict = ?, summary = ?, findings_json = ?,
    sealed_at_ns = ?, lease_owner = '', lease_until_ns = 0, updated_at_ns = ?
WHERE review_key = ? AND state = 'in_progress' AND provider_started = 1
  AND lease_generation = ? AND lease_until_ns <= ?`,
				VerdictError, "sampler lease expired after provider start", []byte("[]"),
				nowNS, nowNS, seed.Key, record.LeaseGeneration, nowNS)
			if err != nil {
				return AcquireResult{}, fmt.Errorf("seal abandoned review: %w", err)
			}
			if n, _ := updated.RowsAffected(); n == 1 {
				sealed, err := s.load(ctx, seed.Key)
				if err != nil {
					return AcquireResult{}, err
				}
				return AcquireResult{State: AcquireSealed, Record: sealed}, nil
			}
			continue
		}

		updated, err := s.db.ExecContext(ctx, `
UPDATE grok_review_records
SET lease_owner = ?, lease_generation = lease_generation + 1,
    lease_until_ns = ?, updated_at_ns = ?
WHERE review_key = ? AND state = 'in_progress' AND provider_started = 0
  AND lease_generation = ? AND lease_until_ns <= ?`,
			owner, leaseNS, nowNS, seed.Key, record.LeaseGeneration, nowNS)
		if err != nil {
			return AcquireResult{}, fmt.Errorf("steal expired review lease: %w", err)
		}
		if n, _ := updated.RowsAffected(); n == 1 {
			stolen, err := s.load(ctx, seed.Key)
			if err != nil {
				return AcquireResult{}, err
			}
			return won(stolen), nil
		}
	}
	return AcquireResult{}, fmt.Errorf("acquire review %q: concurrent update did not settle", seed.Key)
}

func (s *Store) Heartbeat(ctx context.Context, lease Lease, now time.Time, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("heartbeat ttl must be positive")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE grok_review_records
SET lease_until_ns = ?, updated_at_ns = ?
WHERE review_key = ? AND state = 'in_progress' AND lease_owner = ?
  AND lease_generation = ? AND lease_until_ns > ?`,
		now.Add(ttl).UnixNano(), now.UnixNano(), lease.Key, lease.Owner,
		lease.Generation, now.UnixNano())
	return fenceResult(result, err, "heartbeat review lease")
}

func (s *Store) MarkProviderStarted(ctx context.Context, lease Lease, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE grok_review_records
SET provider_started = 1, updated_at_ns = ?
WHERE review_key = ? AND state = 'in_progress' AND lease_owner = ?
  AND lease_generation = ? AND lease_until_ns > ?`,
		now.UnixNano(), lease.Key, lease.Owner, lease.Generation, now.UnixNano())
	return fenceResult(result, err, "mark review provider started")
}

func (s *Store) Seal(ctx context.Context, lease Lease, terminal TerminalResult, now time.Time) error {
	if terminal.Verdict != VerdictApproved && terminal.Verdict != VerdictBlocking && terminal.Verdict != VerdictError {
		return fmt.Errorf("invalid terminal verdict %q", terminal.Verdict)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE grok_review_records
SET state = 'sealed', verdict = ?, summary = ?, findings_json = ?,
    sealed_at_ns = ?, lease_owner = '', lease_until_ns = 0, updated_at_ns = ?
WHERE review_key = ? AND state = 'in_progress' AND lease_owner = ?
  AND lease_generation = ? AND lease_until_ns > ?`,
		terminal.Verdict, terminal.Summary, terminal.FindingsJSON, now.UnixNano(),
		now.UnixNano(), lease.Key, lease.Owner, lease.Generation, now.UnixNano())
	return fenceResult(result, err, "seal review")
}

func (s *Store) NextGeneration(ctx context.Context, head HeadKey) (int64, error) {
	if head.InstallationID <= 0 || head.RepositoryID <= 0 || head.HeadSHA == "" || head.CheckName == "" {
		return 0, fmt.Errorf("head generation key is incomplete")
	}
	var generation int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO grok_review_head_generations (
    installation_id, repository_id, head_sha, check_name, generation
) VALUES (?, ?, ?, ?, 1)
ON CONFLICT (installation_id, repository_id, head_sha, check_name)
DO UPDATE SET generation = generation + 1
RETURNING generation`, head.InstallationID, head.RepositoryID, head.HeadSHA, head.CheckName).Scan(&generation)
	if err != nil {
		return 0, fmt.Errorf("advance review generation: %w", err)
	}
	return generation, nil
}

func (s *Store) load(ctx context.Context, key string) (Record, error) {
	var record Record
	var leaseUntilNS, sealedAtNS int64
	err := s.db.QueryRowContext(ctx, `
SELECT review_key, installation_id, repository_id, repository, pull_request,
       base_ref, base_sha, head_sha, core_hash, policy_hash, adapter_release,
       model_hash, state, lease_owner, lease_generation, lease_until_ns,
       provider_started, verdict, summary, findings_json, sealed_at_ns
FROM grok_review_records WHERE review_key = ?`, key).Scan(
		&record.Key, &record.InstallationID, &record.RepositoryID, &record.Repository,
		&record.PullRequest, &record.BaseRef, &record.BaseSHA, &record.HeadSHA,
		&record.CoreHash, &record.PolicyHash, &record.Release, &record.ModelHash,
		&record.State, &record.Owner, &record.LeaseGeneration, &leaseUntilNS,
		&record.ProviderStarted, &record.Verdict, &record.Summary,
		&record.FindingsJSON, &sealedAtNS)
	if err != nil {
		return Record{}, fmt.Errorf("load review record %q: %w", key, err)
	}
	if leaseUntilNS > 0 {
		record.LeaseUntil = time.Unix(0, leaseUntilNS)
	}
	if sealedAtNS > 0 {
		record.SealedAt = time.Unix(0, sealedAtNS)
	}
	return record, nil
}

func validateAcquire(seed ReviewSeed, owner string, ttl time.Duration) error {
	if seed.Key == "" || seed.InstallationID <= 0 || seed.RepositoryID <= 0 ||
		seed.Repository == "" || seed.PullRequest <= 0 || seed.BaseRef == "" ||
		seed.BaseSHA == "" || seed.HeadSHA == "" || seed.CoreHash == "" ||
		seed.PolicyHash == "" || seed.Release == "" || seed.ModelHash == "" {
		return fmt.Errorf("review seed is incomplete")
	}
	if owner == "" {
		return fmt.Errorf("review lease owner is required")
	}
	if ttl <= 0 {
		return fmt.Errorf("review lease ttl must be positive")
	}
	return nil
}

func sameSeed(left, right ReviewSeed) error {
	if left != right {
		return fmt.Errorf("review key %q is already bound to another immutable tuple", right.Key)
	}
	return nil
}

func won(record Record) AcquireResult {
	return AcquireResult{
		State:  AcquireWon,
		Lease:  Lease{Key: record.Key, Owner: record.Owner, Generation: record.LeaseGeneration},
		Record: record,
	}
}

func fenceResult(result sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s result: %w", action, err)
	}
	if rows != 1 {
		return ErrFenceLost
	}
	return nil
}
