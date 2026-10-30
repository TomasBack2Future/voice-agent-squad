package execution

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// ServiceName identifies this admission service to readiness and listener probes.
const ServiceName = "squad-execution-admission"

// Class is the sanitized reason a verification or readiness step failed. It is
// the only failure detail that leaves the service: never stderr, paths, headers,
// tokens or response bodies.
type Class string

const (
	ClassToolMissing            Class = "tool_missing"
	ClassCredentialMissing      Class = "credential_missing"
	ClassGitHubUnavailable      Class = "github_unavailable"
	ClassRunMismatch            Class = "run_mismatch"
	ClassLedgerUnavailable      Class = "ledger_unavailable"
	ClassAuthorizationDenied    Class = "authorization_rejected"
	ClassDiagnosticsAbsent      Class = "diagnostics_unavailable"
	githubLookupBudget                = 15 * time.Second
	identityDomain                    = "squad-execution-admission/identity\n"
	identityProbeBudget               = 2 * time.Second
	maxIdentityResponse         int64 = 1024
	startupCredentialCheckLimit       = 15 * time.Second
)

// Error carries a Class and nothing else.
type Error struct{ Class Class }

func (e *Error) Error() string { return string(e.Class) }

// ClassOf returns the class of err, or "" when err carries none.
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

// StartupFatal reports whether a startup check failure is a local misconfiguration
// that the service must not run with. A GitHub outage is transient and only
// visible through readiness.
func StartupFatal(err error) bool {
	c := ClassOf(err)
	return c == ClassToolMissing || c == ClassCredentialMissing
}

// classifyGH maps a failed `gh` invocation to a Class. launchd without HOME makes
// gh answer "not logged in" with exit status 4; a PATH without gh is not found.
func classifyGH(ctx context.Context, err error, stderr []byte) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &Error{ClassToolMissing}
	}
	if ctx.Err() != nil {
		return &Error{ClassGitHubUnavailable}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		text := strings.ToLower(string(stderr))
		if exit.ExitCode() == 4 || strings.Contains(text, "gh auth login") || strings.Contains(text, "gh_token") ||
			strings.Contains(text, "bad credentials") || strings.Contains(text, "http 401") || strings.Contains(text, "not logged in") {
			return &Error{ClassCredentialMissing}
		}
		return &Error{ClassGitHubUnavailable}
	}
	return &Error{ClassToolMissing}
}

// VerifyGitHubLookup proves the service's own gh can read the workflow repository's
// Actions runs, the same permission VerifyGitHubRun needs. It is read-only.
func VerifyGitHubLookup(ctx context.Context, repo string) error {
	if !repository.MatchString(repo) {
		return &Error{ClassRunMismatch}
	}
	ctx, cancel := context.WithTimeout(ctx, githubLookupBudget)
	defer cancel()
	_, err := ghOutput(ctx, fmt.Sprintf("repos/%s/actions/runs?per_page=1", repo), ClassGitHubUnavailable)
	return err
}

// CheckStartup verifies, before the service listens, that the process environment
// can locate gh and that gh holds usable credentials. It never prints output.
func CheckStartup(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, startupCredentialCheckLimit)
	defer cancel()
	if _, err := exec.LookPath("gh"); err != nil {
		return &Error{ClassToolMissing}
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.ToLower(stderr.String())
		if strings.Contains(text, "error connecting") || strings.Contains(text, "dial tcp") || strings.Contains(text, "timeout") {
			return &Error{ClassGitHubUnavailable}
		}
		return classifyGH(ctx, err, stderr.Bytes())
	}
	return nil
}

type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(l.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.b = append(l.b, p[:room]...)
	}
	return len(p), nil
}
func (l *limitedBuffer) Bytes() []byte  { return l.b }
func (l *limitedBuffer) String() string { return string(l.b) }

// Readiness is the sanitized, side-effect-free verdict of the authority's own
// environment. It performs no begin, check, pin change or claim write.
type Readiness struct {
	Service   string            `json:"service"`
	Ready     bool              `json:"ready"`
	Class     Class             `json:"class,omitempty"`
	RepoID    string            `json:"repo_id"`
	Checks    map[string]string `json:"checks"`
	CheckedAt int64             `json:"checked_at"`
}

// Diagnose reads the ledger and performs one bounded GitHub lookup using the
// service's actual environment. Reads only: it opens no write transaction.
func (s *Store) Diagnose(ctx context.Context, repo string, lookup func(context.Context, string) error) Readiness {
	out := Readiness{Service: ServiceName, RepoID: s.repoID, Checks: map[string]string{}, CheckedAt: s.now().Unix()}
	var claims, bindings int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND item_id='ENV-002'`, s.repoID).Scan(&claims)
	if err == nil {
		err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM execution_authorizations WHERE repo_id=?`, s.repoID).Scan(&bindings)
	}
	if err != nil {
		out.Checks["ledger"] = string(ClassLedgerUnavailable)
		out.Class = ClassLedgerUnavailable
	} else {
		out.Checks["ledger"] = "ok"
	}
	if lookup == nil {
		out.Checks["github"] = string(ClassDiagnosticsAbsent)
		if out.Class == "" {
			out.Class = ClassDiagnosticsAbsent
		}
	} else if err := lookup(ctx, repo); err != nil {
		c := ClassOf(err)
		if c == "" {
			c = ClassGitHubUnavailable
		}
		out.Checks["github"] = string(c)
		if out.Class == "" {
			out.Class = c
		}
	} else {
		out.Checks["github"] = "ok"
	}
	out.Ready = out.Class == ""
	return out
}

// Listener identity: a challenge/response keyed by the shared token and the
// ledger id lets a starting service tell its own earlier instance from any other
// process on the port without ever sending the token to that process.
func identityProof(token, repoID, nonce string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = io.WriteString(mac, identityDomain+nonce+"\n"+repoID)
	return hex.EncodeToString(mac.Sum(nil))
}

type identityDocument struct {
	Service string `json:"service"`
	Proof   string `json:"proof"`
}

type ListenerIdentity string

const (
	ListenerSameService ListenerIdentity = "same_service"
	ListenerForeign     ListenerIdentity = "foreign_listener"
	ListenerUnreachable ListenerIdentity = "unreachable"
)

// ProbeListener asks whatever answers on addr (plain HTTP, loopback) to prove it
// is this ledger's admission service. Nothing is killed, signalled or authenticated.
func ProbeListener(ctx context.Context, addr, token, repoID string) ListenerIdentity {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return ListenerForeign
	}
	nonce := hex.EncodeToString(nonceBytes)
	ctx, cancel := context.WithTimeout(ctx, identityProbeBudget)
	defer cancel()
	u := url.URL{Scheme: "http", Host: addr, Path: "/v1/executions/identity", RawQuery: url.Values{"nonce": {nonce}}.Encode()}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ListenerForeign
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return ListenerUnreachable
	}
	defer response.Body.Close()
	var doc identityDocument
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, maxIdentityResponse)).Decode(&doc) != nil || doc.Service != ServiceName {
		return ListenerForeign
	}
	if !hmac.Equal([]byte(doc.Proof), []byte(identityProof(token, repoID, nonce))) {
		return ListenerForeign
	}
	return ListenerSameService
}
