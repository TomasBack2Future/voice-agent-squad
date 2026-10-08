package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Remote struct {
	URL, Token string
	HTTP       *http.Client
}

func NewClient(address, token string) (*Remote, error) {
	u, e := url.Parse(address)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid Squad service URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())) {
		return nil, fmt.Errorf("squad remote requires HTTPS (HTTP only for a loopback tunnel)")
	}
	if len(token) < 32 || len(token) > 1017 || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("SQUAD_REMOTE_TOKEN must contain at least 32 characters")
	}
	return &Remote{URL: strings.TrimRight(address, "/"), Token: token, HTTP: &http.Client{Timeout: 130 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func NewRequestID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (c *Remote) Call(ctx context.Context, path, id string, body []byte) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", id)
	res, e := c.HTTP.Do(req)
	if e != nil {
		return nil, fmt.Errorf("remote response unavailable; preserve request key %s and reconcile before retrying", id)
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 12*MaxOutput+1025))
	if e != nil || len(data) > 12*MaxOutput+1024 {
		return nil, fmt.Errorf("invalid remote response; request key %s", id)
	}
	if res.StatusCode == http.StatusAccepted {
		return nil, nil
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("remote HTTP %d; request key %s: %s", res.StatusCode, id, strings.TrimSpace(string(data)))
	}
	return data, nil
}
func (c *Remote) Command(ctx context.Context, args []string, id string) (Result, error) {
	if id == "" {
		var e error
		id, e = NewRequestID()
		if e != nil {
			return Result{}, e
		}
	}
	body, _ := json.Marshal(Command{Args: args})
	raw, e := c.Call(ctx, "/v1/command", id, body)
	if e != nil {
		return Result{}, e
	}
	var result Result
	if e = json.Unmarshal(raw, &result); e != nil {
		return Result{}, fmt.Errorf("invalid command response; request key %s", id)
	}
	return result, nil
}
func (c *Remote) MCP(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), MaxBody)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		id, e := NewRequestID()
		if e != nil {
			return e
		}
		body, e := c.Call(ctx, "/mcp", id, scanner.Bytes())
		if e != nil {
			return e
		}
		if len(body) > 0 {
			if _, e = fmt.Fprintln(out, string(body)); e != nil {
				return e
			}
		}
	}
	return scanner.Err()
}
