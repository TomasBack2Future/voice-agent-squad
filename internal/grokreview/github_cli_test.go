package grokreview

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type recordedCommand struct {
	args  []string
	stdin []byte
	env   []string
}

type fakeCommandExecutor struct {
	calls   []recordedCommand
	outputs [][]byte
	errors  []error
}

type fakeHTTPDoer struct {
	request  *http.Request
	response *http.Response
	err      error
}

func (f *fakeHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	f.request = request.Clone(request.Context())
	return f.response, f.err
}

func (f *fakeCommandExecutor) run(_ context.Context, args []string, stdin []byte, env []string) ([]byte, error) {
	f.calls = append(f.calls, recordedCommand{
		args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...), env: append([]string(nil), env...),
	})
	index := len(f.calls) - 1
	var output []byte
	if index < len(f.outputs) {
		output = f.outputs[index]
	}
	if index < len(f.errors) {
		return output, f.errors[index]
	}
	return output, nil
}

func TestMintAppJWTBindsIssuerAndLifetime(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Unix(1_800_000_000, 0)
	token, err := MintAppJWT(4862345, pemKey, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT parts = %d", len(parts))
	}
	var claims struct {
		Issuer string `json:"iss"`
		Issued int64  `json:"iat"`
		Expiry int64  `json:"exp"`
	}
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "4862345" || claims.Issued != now.Add(-time.Minute).Unix() || claims.Expiry != now.Add(9*time.Minute).Unix() {
		t.Fatalf("claims = %#v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("verify signature: %v", err)
	}
}

func TestMintInstallationTokenUsesBearerWithoutCallingGitHubCLI(t *testing.T) {
	executor := &fakeCommandExecutor{outputs: [][]byte{[]byte(`{"token":"installation-token","expires_at":"2026-09-08T01:00:00Z"}`)}}
	client := testGitHubCLI(executor)
	doer := &fakeHTTPDoer{response: &http.Response{
		StatusCode: http.StatusCreated,
		Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token","expires_at":"2026-09-08T01:00:00Z"}`)),
	}}
	client.httpClient = doer
	token, err := client.MintInstallationToken(context.Background(), "signed-jwt", 42)
	if err != nil {
		t.Fatal(err)
	}
	if token != "installation-token" {
		t.Fatalf("token = %q", token)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("GitHub CLI calls = %d", len(executor.calls))
	}
	if doer.request == nil {
		t.Fatal("installation-token request was not sent")
	}
	if doer.request.Method != http.MethodPost || doer.request.URL.String() != "https://api.github.test/app/installations/42/access_tokens" {
		t.Fatalf("request = %s %s", doer.request.Method, doer.request.URL)
	}
	if got := doer.request.Header.Get("Authorization"); got != "Bearer signed-jwt" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := doer.request.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Fatalf("X-GitHub-Api-Version = %q", got)
	}
}

func TestFetchPullRequestReturnsMetadataAndDiff(t *testing.T) {
	executor := &fakeCommandExecutor{outputs: [][]byte{
		[]byte(`{"number":17,"title":"fix bug","body":"details","base":{"ref":"main","sha":"base123"},"head":{"sha":"head456"}}`),
		[]byte("diff --git a/a.go b/a.go\n+fixed\n"),
	}}
	client := testGitHubCLI(executor)
	snapshot, err := client.FetchPullRequest(context.Background(), "owner/repo", 17, "app-token")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HeadSHA != "head456" || snapshot.BaseSHA != "base123" || snapshot.Diff == "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	for _, call := range executor.calls {
		if !containsExact(call.env, "GH_TOKEN=app-token") {
			t.Fatalf("app token missing from environment: %#v", call.env)
		}
		if strings.Contains(strings.Join(call.args, " "), "app-token") {
			t.Fatal("app token leaked into arguments")
		}
	}
}

func TestPublishReviewCreatesAppCommentAndSHAResultCheck(t *testing.T) {
	executor := &fakeCommandExecutor{outputs: [][]byte{
		[]byte(`{"id":101,"html_url":"https://github.test/comment/101"}`),
		[]byte(`{"id":202,"html_url":"https://github.test/check/202"}`),
	}}
	client := testGitHubCLI(executor)
	snapshot := PullRequestSnapshot{
		Repository: "owner/repo", Number: 17, BaseRef: "main",
		BaseSHA: "base123", HeadSHA: "head456", Title: "fix bug", Diff: "+fixed",
	}
	result := FindingsResult{
		SchemaVersion: FindingsSchemaVersion,
		Verdict:       VerdictApproved,
		Summary:       "No blocking findings.",
		Findings:      []Finding{},
	}
	publication, err := client.PublishReview(context.Background(), "app-token", "grok-review-shadow", snapshot, result, CLIAudit{
		RequestID: "request-1", SessionID: "session-1", RequestedModel: "grok-4.6", ResolvedModel: "grok-4.6-build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if publication.CommentID != 101 || publication.CheckRunID != 202 || publication.Conclusion != "success" {
		t.Fatalf("publication = %#v", publication)
	}
	if len(executor.calls) != 2 {
		t.Fatalf("calls = %d", len(executor.calls))
	}
	var comment map[string]any
	if err := json.Unmarshal(executor.calls[0].stdin, &comment); err != nil {
		t.Fatal(err)
	}
	body, _ := comment["body"].(string)
	if !strings.Contains(body, "head456") || !strings.Contains(body, "No blocking findings") {
		t.Fatalf("comment body = %q", body)
	}
	var check struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(executor.calls[1].stdin, &check); err != nil {
		t.Fatal(err)
	}
	if check.Name != "grok-review-shadow" || check.HeadSHA != "head456" || check.Conclusion != "success" {
		t.Fatalf("check = %#v", check)
	}
}

func TestPublishReviewMapsBlockingToFailure(t *testing.T) {
	executor := &fakeCommandExecutor{outputs: [][]byte{[]byte(`{"id":1}`), []byte(`{"id":2}`)}}
	client := testGitHubCLI(executor)
	result := FindingsResult{
		SchemaVersion: FindingsSchemaVersion,
		Verdict:       VerdictBlocking,
		Summary:       "One blocking finding.",
		Findings: []Finding{{
			Category: "correctness", Severity: "high", Blocking: true,
			Path: "main.go", Line: 9, Title: "race", Body: "shared state is unsynchronized",
		}},
	}
	publication, err := client.PublishReview(context.Background(), "token", "grok-review", PullRequestSnapshot{
		Repository: "owner/repo", Number: 3, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "title", Diff: "+x",
	}, result, CLIAudit{})
	if err != nil {
		t.Fatal(err)
	}
	if publication.Conclusion != "failure" {
		t.Fatalf("conclusion = %q", publication.Conclusion)
	}
}

func TestGitHubCLIRejectsInvalidRepositoryBeforeCallingCommand(t *testing.T) {
	executor := &fakeCommandExecutor{}
	client := testGitHubCLI(executor)
	_, err := client.FetchPullRequest(context.Background(), "owner/repo/extra", 1, "token")
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("error = %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("calls = %d", len(executor.calls))
	}
}

func TestMintInstallationTokenPropagatesTransportFailureWithoutJWT(t *testing.T) {
	executor := &fakeCommandExecutor{}
	client := testGitHubCLI(executor)
	client.httpClient = &fakeHTTPDoer{err: errors.New("transport failed")}
	_, err := client.MintInstallationToken(context.Background(), "sensitive-jwt", 42)
	if err == nil || !strings.Contains(err.Error(), "transport failed") || strings.Contains(err.Error(), "sensitive-jwt") {
		t.Fatalf("error = %v", err)
	}
}

func TestPublishedTextIsBoundedAndValidUTF8(t *testing.T) {
	finding := Finding{
		Category: "correctness", Severity: "high", Blocking: true,
		Path: "main.go", Line: 9, Title: "问题", Body: strings.Repeat("界", 30_000),
	}
	rendered := renderFindings([]Finding{finding, finding})
	if len(rendered) > 60_000 {
		t.Fatalf("rendered findings bytes = %d", len(rendered))
	}
	if !utf8.ValidString(rendered) {
		t.Fatal("rendered findings are not valid UTF-8")
	}
}

func testGitHubCLI(executor *fakeCommandExecutor) *GitHubCLI {
	return &GitHubCLI{
		binary: "/opt/homebrew/bin/gh", timeout: time.Minute,
		maxOutputBytes: 1 << 20, execute: executor.run,
		httpClient: &fakeHTTPDoer{err: errors.New("unexpected HTTP call")},
		apiBaseURL: "https://api.github.test",
	}
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
