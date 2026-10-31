package dispatch

import (
	"strings"
	"testing"
	"time"
)

const (
	gateHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gateBase = "cccccccccccccccccccccccccccccccccccccccc"
)

// gateLedger: Worker "worker" holds its claim under reservation D generation 1,
// native new-native, with a decision at revision 3.
func gateLedger(t *testing.T, policy, action string, extra ...string) *Store {
	t.Helper()
	now := time.Unix(10000, 0)
	s := newDispatchStore(t, &now)
	stmts := append([]string{
		`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','dispatcher',1,1,0,'dispatched',1,'new-native','','T')`,
		`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','worker',2,2,'held',1)`,
		`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,merge_policy) VALUES('repo-test','D',1,'T',3,0,'` + action + `','c','worker','` + policy + `')`,
	}, extra...)
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return s
}

func release(revision int, head string, expires int64) string {
	return `INSERT INTO dispatch_merge_releases(repo_id,reservation_key,generation,item_id,decision_revision,pr_repo,pr_number,head_sha,base_sha,evidence_outcome,expires_at) VALUES('repo-test','D',1,'T',` +
		string(rune('0'+revision)) + `,'o/r',7,'` + head + `','` + gateBase + `',1,` + itoa(expires) + `)`
}

func itoa(v int64) string {
	b := []byte{}
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if len(b) == 0 {
		return "0"
	}
	return string(b)
}

func bash(c string) NativeHookTool { return NativeHookTool{Name: "Bash", Command: c} }

const plain = "gh pr merge 7 --repo o/r --squash --match-head-commit " + gateHead

func TestReleaseRequiredAssignmentsCannotMergeWithoutAnExactCurrentRelease(t *testing.T) {
	good := release(3, gateHead, 20000)
	cases := []struct {
		name, policy, action string
		extra                []string
		tool                 NativeHookTool
		ok                   bool
	}{
		{"no release", "release-required", "proceed", nil, bash(plain), false},
		{"exact release", "release-required", "proceed", []string{good}, bash(plain), true},
		{"exact release, delete branch", "release-required", "proceed", []string{good}, bash(plain + " --delete-branch"), true},
		{"different head in command", "release-required", "proceed", []string{good}, bash(strings.Replace(plain, gateHead, "dddddddddddddddddddddddddddddddddddddddd", 1)), false},
		{"different PR number", "release-required", "proceed", []string{good}, bash(strings.Replace(plain, " 7 ", " 8 ", 1)), false},
		{"different repository", "release-required", "proceed", []string{good}, bash(strings.Replace(plain, "o/r", "o/other", 1)), false},
		{"release head differs from command", "release-required", "proceed", []string{release(3, "dddddddddddddddddddddddddddddddddddddddd", 20000)}, bash(plain), false},
		{"release of an older revision", "release-required", "proceed", []string{release(2, gateHead, 20000)}, bash(plain), false},
		{"expired release", "release-required", "proceed", []string{release(3, gateHead, 9000)}, bash(plain), false},
		{"hold decision", "release-required", "hold", []string{good}, bash(plain), false},
		{"no head guard", "release-required", "proceed", []string{good}, bash("gh pr merge 7 --repo o/r --squash"), false},
		{"admin bypass", "release-required", "proceed", []string{good}, bash(plain + " --admin"), false},
		{"auto merge", "release-required", "proceed", []string{good}, bash(plain + " --auto"), false},
		{"two strategies", "release-required", "proceed", []string{good}, bash(plain + " --merge"), false},
		{"chained command", "release-required", "proceed", []string{good}, bash(plain + "; rm -rf x"), false},
		{"inside shell string", "release-required", "proceed", []string{good}, bash("bash -c '" + plain + "'"), false},
		{"api merge route", "release-required", "proceed", []string{good}, bash("gh api -X PUT repos/o/r/pulls/7/merge -f sha=" + gateHead), false},
		{"graphql merge", "release-required", "proceed", []string{good}, bash("gh api graphql -f query='mutation{mergePullRequest(input:{pullRequestId:\"x\"}){clientMutationId}}'"), false},
		{"push to main", "release-required", "proceed", []string{good}, bash("git push origin HEAD:main"), false},
		{"merge mcp tool", "release-required", "proceed", []string{good}, NativeHookTool{Name: "mcp__github__merge_pull_request"}, false},
		{"branch merges endpoint", "release-required", "proceed", []string{good}, bash("gh api -X POST repos/o/r/merges -f base=main -f head=x"), false},
		{"ref update", "release-required", "proceed", []string{good}, bash("gh api -X PATCH repos/o/r/git/refs/heads/main -f sha=x"), false},
		{"curl merge", "release-required", "proceed", []string{good}, bash("curl -X PUT https://api.github.com/repos/o/r/pulls/7/merge"), false},
		{"interpreter argv list", "release-required", "proceed", []string{good}, bash(`python3 -c "import subprocess; subprocess.run(['gh','pr','merge','7'])"`), false},
		{"quoted subcommand", "release-required", "proceed", []string{good}, bash("gh pr 'merge' 7 --squash"), false},
		{"repo flag before subcommand", "release-required", "proceed", []string{good}, bash("gh pr -R o/r merge 7 --squash"), false},
		{"alias route", "release-required", "proceed", []string{good}, bash("gh alias set m 'pr merge'"), false},
		{"continued api line", "release-required", "proceed", []string{good}, bash("gh api \\\n repos/o/r/pulls/7/merge -X PUT"), false},
		{"bare push", "release-required", "proceed", []string{good}, bash("git push"), false},
		{"push HEAD", "release-required", "proceed", []string{good}, bash("git push -u origin HEAD"), false},
		{"push to develop", "release-required", "proceed", []string{good}, bash("git push origin HEAD:refs/heads/develop"), false},
		{"force push main", "release-required", "proceed", []string{good}, bash("git push --force origin +feature:main"), false},
		{"git -C push main", "release-required", "proceed", []string{good}, bash("git -C /w push origin main"), false},
		{"git -c push main", "release-required", "proceed", []string{good}, bash("git -c a=b push origin HEAD:main"), false},
		{"quoted main", "release-required", "proceed", []string{good}, bash("git push origin 'main'"), false},
		{"quoted refspec", "release-required", "proceed", []string{good}, bash(`git push origin "HEAD:main"`), false},
		{"git -C feature push", "release-required", "proceed", nil, bash("git -C /w push -u origin squad/fix-1"), true},
		{"commit message mentioning push", "release-required", "proceed", nil, bash(`git commit -m "fix: push to main handling"`), true},
		{"commit message exactly push", "release-required", "proceed", nil, bash(`git commit -m "push"`), true},
		{"log grep push", "release-required", "proceed", nil, bash("git log --grep=push"), true},
		{"git --no-pager push main", "release-required", "proceed", []string{good}, bash("git --no-pager push origin main"), false},
		{"branch containing main", "release-required", "proceed", nil, bash("git push -u origin squad/main-gate"), true},
		{"repo name case differs", "release-required", "proceed", []string{good}, bash(strings.Replace(plain, "o/r", "O/R", 1)), true},
		{"unrelated bash", "release-required", "proceed", nil, bash("go test ./..."), true},
		{"pr view is not a merge", "release-required", "proceed", nil, bash("gh pr view 7 --json state"), true},
		{"push feature branch", "release-required", "proceed", nil, bash("git push origin claude/fix-7"), true},
		{"standing policy unchanged", "standing", "proceed", nil, bash(plain), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := gateLedger(t, c.policy, c.action, c.extra...)
			err := record(s, "worker", "new-native", c.tool)
			if (err == nil) != c.ok {
				t.Fatalf("ok=%v err=%v", c.ok, err)
			}
			if err != nil && !strings.Contains(err.Error(), "merge-release-required") {
				t.Fatalf("denial must name the repair, got %v", err)
			}
		})
	}
}

func TestMergeGateDoesNotAffectABootstrappingOrForeignNative(t *testing.T) {
	s := gateLedger(t, "release-required", "proceed", release(3, gateHead, 20000))
	if err := record(s, "worker", "other-native", bash(plain)); err == nil {
		t.Fatal("a native bound to no assignment merged")
	}
}

func TestPolicyCarriesToTheNextGenerationOfTheSameAssignment(t *testing.T) {
	s := gateLedger(t, "release-required", "proceed", release(3, gateHead, 20000))
	// A replacement Worker at generation 2 has no decision row yet.
	for _, q := range []string{
		`UPDATE dispatch_reservations SET generation=2,worker_thread_id='next-native'`,
		`DELETE FROM claims`,
		`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','worker2',3,3,'held',2)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := record(s, "worker2", "next-native", bash(plain)); err == nil || !strings.Contains(err.Error(), "merge-release-required") {
		t.Fatalf("policy lost across generations: %v", err)
	}
	if err := record(s, "worker2", "next-native", bash("go test ./...")); err != nil {
		t.Fatal(err)
	}
}
