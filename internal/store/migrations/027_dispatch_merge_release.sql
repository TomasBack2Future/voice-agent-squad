-- A merge release is structured authority for one exact pull request head, bound
-- to one decision revision. A later revision, hold or expiry revokes it; the
-- decision prose never grants merge permission. merge_policy 'release-required'
-- makes the native tool boundary refuse merge-class commands without a release.
ALTER TABLE dispatch_decisions ADD COLUMN merge_policy TEXT NOT NULL DEFAULT 'standing';
CREATE TABLE dispatch_merge_releases (
 repo_id TEXT NOT NULL,
 reservation_key TEXT NOT NULL,
 generation INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 decision_revision INTEGER NOT NULL,
 pr_repo TEXT NOT NULL,
 pr_number INTEGER NOT NULL CHECK(pr_number > 0),
 head_sha TEXT NOT NULL,
 base_sha TEXT NOT NULL,
 evidence_outcome INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 PRIMARY KEY(repo_id,reservation_key,generation,item_id,pr_repo,pr_number)
) STRICT;
