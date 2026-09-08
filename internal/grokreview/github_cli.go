package grokreview

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type PullRequestSnapshot struct {
	Repository  string `json:"repository"`
	Number      int    `json:"pull_request"`
	BaseRef     string `json:"base_ref"`
	BaseSHA     string `json:"base_sha"`
	HeadSHA     string `json:"head_sha"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Diff        string `json:"diff"`
}

type Publication struct {
	CommentID  int64
	CommentURL string
	CheckRunID int64
	CheckURL   string
	Conclusion string
}

type commandExecutor func(context.Context, []string, []byte, []string) ([]byte, error)

type GitHubCLI struct {
	binary         string
	timeout        time.Duration
	maxOutputBytes int
	execute        commandExecutor
}

func NewGitHubCLI(binary string, timeout time.Duration, maxOutputBytes int) (*GitHubCLI, error) {
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("GitHub CLI binary must be an absolute path")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("GitHub CLI timeout must be positive")
	}
	if maxOutputBytes <= 0 {
		return nil, fmt.Errorf("GitHub CLI output limit must be positive")
	}
	client := &GitHubCLI{binary: binary, timeout: timeout, maxOutputBytes: maxOutputBytes}
	client.execute = client.runCommand
	return client, nil
}

func MintAppJWT(appID int64, privateKeyPEM []byte, now time.Time) (string, error) {
	if appID <= 0 {
		return "", fmt.Errorf("GitHub App ID must be positive")
	}
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return "", fmt.Errorf("decode GitHub App private key PEM")
	}
	var key *rsa.PrivateKey
	parsedPKCS1, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		key = parsedPKCS1
	} else {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return "", fmt.Errorf("parse GitHub App private key: %w", err)
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("GitHub App private key is not RSA")
		}
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		Issuer string `json:"iss"`
		Issued int64  `json:"iat"`
		Expiry int64  `json:"exp"`
	}{
		Issuer: strconv.FormatInt(appID, 10),
		Issued: now.Add(-time.Minute).Unix(),
		Expiry: now.Add(9 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claims)
	signingInput := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (g *GitHubCLI) MintInstallationToken(ctx context.Context, appJWT string, installationID int64) (string, error) {
	if appJWT == "" || installationID <= 0 {
		return "", fmt.Errorf("GitHub App JWT and installation ID are required")
	}
	endpoint := "app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	raw, err := g.call(ctx, appJWT, []string{"api", "--method", "POST", endpoint}, nil)
	if err != nil {
		return "", fmt.Errorf("mint GitHub App installation token: %w", err)
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("parse GitHub App installation token: %w", err)
	}
	if response.Token == "" {
		return "", fmt.Errorf("GitHub App installation token response is empty")
	}
	return response.Token, nil
}

func (g *GitHubCLI) FetchPullRequest(ctx context.Context, repository string, number int, token string) (PullRequestSnapshot, error) {
	if err := validateRepository(repository); err != nil {
		return PullRequestSnapshot{}, err
	}
	if number <= 0 || token == "" {
		return PullRequestSnapshot{}, fmt.Errorf("pull request number and GitHub token are required")
	}
	endpoint := "repos/" + repository + "/pulls/" + strconv.Itoa(number)
	metadata, err := g.call(ctx, token, []string{"api", endpoint}, nil)
	if err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("read pull request metadata: %w", err)
	}
	var response struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Base   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(metadata, &response); err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("parse pull request metadata: %w", err)
	}
	if response.Number != number || response.Base.Ref == "" || response.Base.SHA == "" || response.Head.SHA == "" {
		return PullRequestSnapshot{}, fmt.Errorf("pull request metadata is incomplete")
	}
	diff, err := g.call(ctx, token, []string{"api", endpoint, "-H", "Accept: application/vnd.github.v3.diff"}, nil)
	if err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("read pull request diff: %w", err)
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		return PullRequestSnapshot{}, fmt.Errorf("pull request diff is empty")
	}
	return PullRequestSnapshot{
		Repository: repository, Number: number, BaseRef: response.Base.Ref,
		BaseSHA: response.Base.SHA, HeadSHA: response.Head.SHA,
		Title: response.Title, Description: response.Body, Diff: string(diff),
	}, nil
}

func (g *GitHubCLI) FetchPullRequestIdentity(ctx context.Context, repository string, number int, token string) (PullRequestSnapshot, error) {
	if err := validateRepository(repository); err != nil {
		return PullRequestSnapshot{}, err
	}
	endpoint := "repos/" + repository + "/pulls/" + strconv.Itoa(number)
	metadata, err := g.call(ctx, token, []string{"api", endpoint}, nil)
	if err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("re-read pull request metadata: %w", err)
	}
	var response struct {
		Number int `json:"number"`
		Base   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(metadata, &response); err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("parse current pull request metadata: %w", err)
	}
	if response.Number != number || response.Base.Ref == "" || response.Base.SHA == "" || response.Head.SHA == "" {
		return PullRequestSnapshot{}, fmt.Errorf("current pull request metadata is incomplete")
	}
	return PullRequestSnapshot{
		Repository: repository, Number: number, BaseRef: response.Base.Ref,
		BaseSHA: response.Base.SHA, HeadSHA: response.Head.SHA,
	}, nil
}

func (g *GitHubCLI) PublishReview(ctx context.Context, token, checkName string, snapshot PullRequestSnapshot, result FindingsResult, audit CLIAudit) (Publication, error) {
	if token == "" {
		return Publication{}, fmt.Errorf("GitHub App installation token is required")
	}
	if checkName != "grok-review" && checkName != "grok-review-shadow" {
		return Publication{}, fmt.Errorf("unsupported Grok review Check name %q", checkName)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return Publication{}, err
	}
	if err := ValidateFindings(result); err != nil {
		return Publication{}, err
	}
	conclusion := "failure"
	if result.Verdict == VerdictApproved {
		conclusion = "success"
	}
	body := renderComment(snapshot, result, audit)
	commentInput, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return Publication{}, err
	}
	commentEndpoint := "repos/" + snapshot.Repository + "/issues/" + strconv.Itoa(snapshot.Number) + "/comments"
	commentRaw, err := g.call(ctx, token, []string{"api", "--method", "POST", commentEndpoint, "--input", "-"}, commentInput)
	if err != nil {
		return Publication{}, fmt.Errorf("publish Grok review comment: %w", err)
	}
	var comment struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(commentRaw, &comment); err != nil {
		return Publication{}, fmt.Errorf("parse Grok review comment response: %w", err)
	}

	checkInput, err := json.Marshal(map[string]any{
		"name": checkName, "head_sha": snapshot.HeadSHA, "status": "completed",
		"conclusion":  conclusion,
		"external_id": "local-grok:" + snapshot.Repository + ":" + strconv.Itoa(snapshot.Number) + ":" + snapshot.HeadSHA,
		"details_url": comment.HTMLURL,
		"output": map[string]string{
			"title":   "Grok review " + string(result.Verdict),
			"summary": safeText(result.Summary),
			"text":    renderFindings(result.Findings),
		},
	})
	if err != nil {
		return Publication{}, err
	}
	checkEndpoint := "repos/" + snapshot.Repository + "/check-runs"
	checkRaw, err := g.call(ctx, token, []string{"api", "--method", "POST", checkEndpoint, "--input", "-"}, checkInput)
	if err != nil {
		return Publication{}, fmt.Errorf("publish Grok review Check: %w", err)
	}
	var check struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(checkRaw, &check); err != nil {
		return Publication{}, fmt.Errorf("parse Grok review Check response: %w", err)
	}
	if comment.ID <= 0 || check.ID <= 0 {
		return Publication{}, fmt.Errorf("GitHub publication response lacks comment or Check identity")
	}
	return Publication{
		CommentID: comment.ID, CommentURL: comment.HTMLURL,
		CheckRunID: check.ID, CheckURL: check.HTMLURL, Conclusion: conclusion,
	}, nil
}

func (g *GitHubCLI) call(ctx context.Context, token string, args []string, stdin []byte) ([]byte, error) {
	if token == "" {
		return nil, fmt.Errorf("GitHub token is required")
	}
	environment := []string{
		"GH_TOKEN=" + token,
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
	}
	callCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	output, err := g.execute(callCtx, args, stdin, environment)
	if err != nil {
		return nil, fmt.Errorf("GitHub CLI command failed: %w", err)
	}
	if len(output) > g.maxOutputBytes {
		return nil, fmt.Errorf("GitHub CLI output exceeded %d bytes", g.maxOutputBytes)
	}
	return output, nil
}

func (g *GitHubCLI) runCommand(ctx context.Context, args []string, stdin []byte, environment []string) ([]byte, error) {
	command := exec.CommandContext(ctx, g.binary, args...)
	command.Env = environment
	command.Stdin = bytes.NewReader(stdin)
	stdout := &cappedBuffer{limit: g.maxOutputBytes}
	stderr := &cappedBuffer{limit: 4096}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.overflow {
		return nil, fmt.Errorf("GitHub CLI stdout exceeded limit")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func validateRepository(repository string) error {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(repository, " \\?#") {
		return fmt.Errorf("repository must be owner/name")
	}
	return nil
}

func validateSnapshot(snapshot PullRequestSnapshot) error {
	if err := validateRepository(snapshot.Repository); err != nil {
		return err
	}
	if snapshot.Number <= 0 || snapshot.BaseRef == "" || snapshot.BaseSHA == "" || snapshot.HeadSHA == "" {
		return fmt.Errorf("pull request snapshot identity is incomplete")
	}
	return nil
}

func renderComment(snapshot PullRequestSnapshot, result FindingsResult, audit CLIAudit) string {
	var body strings.Builder
	body.WriteString("<!-- squad-grok-review:")
	body.WriteString(snapshot.HeadSHA)
	body.WriteString(" -->\n## Grok review: ")
	body.WriteString(safeText(string(result.Verdict)))
	body.WriteString("\n\n")
	body.WriteString(safeText(result.Summary))
	body.WriteString("\n\n- Head: `")
	body.WriteString(snapshot.HeadSHA)
	body.WriteString("`\n- Base: `")
	body.WriteString(snapshot.BaseSHA)
	body.WriteString("`\n- Model: `")
	body.WriteString(safeText(audit.ResolvedModel))
	body.WriteString("`\n- Request: `")
	body.WriteString(safeText(audit.RequestID))
	body.WriteString("`\n\n")
	body.WriteString(renderFindings(result.Findings))
	return truncateUTF8(body.String(), 60_000)
}

func renderFindings(findings []Finding) string {
	if len(findings) == 0 {
		return "No findings."
	}
	var body strings.Builder
	for index, finding := range findings {
		body.WriteString(strconv.Itoa(index + 1))
		body.WriteString(". **")
		body.WriteString(safeText(finding.Title))
		body.WriteString("** (`")
		body.WriteString(safeText(finding.Path))
		body.WriteString(":")
		body.WriteString(strconv.Itoa(finding.Line))
		body.WriteString("`, ")
		body.WriteString(safeText(finding.Category))
		body.WriteString(", ")
		body.WriteString(safeText(finding.Severity))
		body.WriteString(")\n   ")
		body.WriteString(safeText(finding.Body))
		body.WriteString("\n")
	}
	return truncateUTF8(body.String(), 60_000)
}

func safeText(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, "@", "@\u200b")
	return truncateUTF8(escaped, 60_000)

}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
