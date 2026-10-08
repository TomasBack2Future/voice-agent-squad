package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const MaxBody = 1 << 20
const MaxOutput = 4 << 20

type Client struct {
	ID          string `json:"id"`
	TokenSHA256 string `json:"token_sha256"`
	Agent       string `json:"agent"`
	Session     string `json:"session"`
	Role        string `json:"role"`
}
type Config struct {
	Workspace string   `json:"workspace"`
	Home      string   `json:"home"`
	Receipts  string   `json:"receipts"`
	Clients   []Client `json:"clients"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`)

func (c Config) Validate() error {
	for _, p := range []string{c.Workspace, c.Home, c.Receipts} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("service paths must be absolute")
		}
	}
	if len(c.Clients) == 0 {
		return fmt.Errorf("at least one client required")
	}
	ids, actors, sessions, tokens := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, v := range c.Clients {
		hash, e := hex.DecodeString(v.TokenSHA256)
		if !identifier.MatchString(v.ID) || !identifier.MatchString(v.Agent) || !identifier.MatchString(v.Session) || e != nil || len(hash) != 32 || v.TokenSHA256 != hex.EncodeToString(hash) {
			return fmt.Errorf("invalid client configuration")
		}
		if !contains([]string{"observer", "worker", "controller"}, v.Role) {
			return fmt.Errorf("invalid client role")
		}
		if ids[v.ID] || actors[v.Agent] || sessions[v.Session] || tokens[v.TokenSHA256] {
			return fmt.Errorf("duplicate client identity or credential")
		}
		ids[v.ID], actors[v.Agent], sessions[v.Session], tokens[v.TokenSHA256] = true, true, true, true
	}
	return nil
}

type Command struct {
	Args []string `json:"args"`
}
type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Runner must isolate identity per invocation and return an error if execution
// might have been interrupted. Such a request is never replayed automatically.
type Runner func(context.Context, Client, []string, []byte) (Result, error)
type Server struct {
	config    Config
	run       Runner
	version   string
	timeout   time.Duration
	queue     chan struct{}
	execution chan struct{}
}

func NewServer(c Config, version string, run Runner) (*Server, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(c.Receipts, 0700); e != nil {
		return nil, e
	}
	return &Server{config: c, version: version, run: run, timeout: 2 * time.Minute, queue: make(chan struct{}, 32), execution: make(chan struct{}, 1)}, nil
}
func (s *Server) authenticate(r *http.Request) (Client, bool) {
	auth := r.Header.Get("Authorization")
	if len(auth) < 39 || len(auth) > 1024 || len(auth) < 7 || auth[:7] != "Bearer " {
		return Client{}, false
	}
	sum := sha256.Sum256([]byte(auth[7:]))
	encoded := hex.EncodeToString(sum[:])
	for _, c := range s.config.Clients {
		if subtle.ConstantTimeCompare([]byte(encoded), []byte(c.TokenSHA256)) == 1 {
			return c, true
		}
	}
	return Client{}, false
}
func respond(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func failure(w http.ResponseWriter, status int, message string) {
	b, _ := json.Marshal(map[string]string{"error": message})
	respond(w, status, b)
}

type receipt struct {
	Hash     string          `json:"hash"`
	Complete bool            `json:"complete"`
	Status   int             `json:"status"`
	Body     json.RawMessage `json:"body,omitempty"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		b, _ := json.Marshal(map[string]string{"status": "ok", "version": s.version})
		respond(w, 200, b)
		return
	}
	c, ok := s.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		failure(w, 401, "authentication required")
		return
	}
	if r.Method != http.MethodPost {
		failure(w, 405, "POST required")
		return
	}
	if r.URL.Path != "/v1/command" && r.URL.Path != "/mcp" {
		failure(w, 404, "unknown endpoint")
		return
	}
	// Browser-origin requests are not an allowed administration surface. Tokens
	// must be supplied by an authenticated CLI/MCP client, never ambient cookies.
	if r.Header.Get("Origin") != "" {
		failure(w, 403, "browser origin not allowed")
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if e != nil {
		failure(w, 413, "request too large")
		return
	}
	args := []string{"mcp"}
	var stdin []byte
	if r.URL.Path == "/v1/command" {
		var command Command
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if dec.Decode(&command) != nil || dec.Decode(new(any)) != io.EOF {
			failure(w, 400, "invalid command request")
			return
		}
		args = command.Args
		if e = CheckCommand(c, args); e != nil {
			failure(w, 403, e.Error())
			return
		}
	} else {
		// Parse once into a value tree before validation and forwarding. Go's
		// map and struct decoders otherwise disagree on duplicate/null keys.
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var parsed any
		if dec.Decode(&parsed) != nil || dec.Decode(new(any)) != io.EOF {
			failure(w, 400, "invalid JSON-RPC request")
			return
		}
		body, e = json.Marshal(parsed)
		if e != nil {
			failure(w, 400, "invalid JSON-RPC request")
			return
		}
		notification, err := CheckMCP(c, body)
		if err != nil {
			failure(w, 403, err.Error())
			return
		}
		if notification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		stdin = append(append([]byte{}, body...), '\n')
	}
	id := r.Header.Get("Idempotency-Key")
	// Standard MCP clients do not supply the extension header. Their underlying
	// coordination operations retain native fences; callers needing replay of a
	// lost response supply the optional key. CLI always supplies one.
	if id == "" && r.URL.Path == "/v1/command" {
		failure(w, 400, "Idempotency-Key required")
		return
	}
	if id != "" && !requestID.MatchString(id) {
		failure(w, 400, "invalid Idempotency-Key")
		return
	}
	// Bound admitted concurrency and serialize legacy file mutations as well as
	// SQLite transitions. Waiting clients may cancel before admission.
	select {
	case s.queue <- struct{}{}:
		defer func() { <-s.queue }()
	default:
		failure(w, 503, "service busy; request not admitted")
		return
	}
	select {
	case s.execution <- struct{}{}:
		defer func() { <-s.execution }()
	case <-r.Context().Done():
		return
	}
	var path, hash string
	if id != "" {
		key := sha256.Sum256([]byte(c.ID + "\x00" + id))
		path = filepath.Join(s.config.Receipts, hex.EncodeToString(key[:])+".json")
		pin, _ := json.Marshal(c)
		digest := sha256.Sum256(append(append([]byte(r.URL.Path), pin...), body...))
		hash = hex.EncodeToString(digest[:])
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			old, readErr := os.ReadFile(path)
			var saved receipt
			if readErr != nil || json.Unmarshal(old, &saved) != nil {
				failure(w, 409, "request outcome unknown; reconcile before any new request")
				return
			}
			if saved.Hash != hash {
				failure(w, 409, "Idempotency-Key already used for different input")
				return
			}
			if !saved.Complete {
				failure(w, 409, "request pending or outcome unknown; reconcile before any new request")
				return
			}
			respond(w, saved.Status, saved.Body)
			return
		}
		if err != nil {
			failure(w, 503, "cannot persist request admission")
			return
		}
		err = json.NewEncoder(file).Encode(receipt{Hash: hash})
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil || syncDir(s.config.Receipts) != nil {
			failure(w, 503, "cannot persist request admission")
			return
		}
	}
	// Finish admitted work even if the HTTP client disconnects. A shutdown waits
	// for handlers; a hard interruption leaves a durable uncertain receipt.
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	result, err := s.run(ctx, c, args, stdin)
	if err != nil {
		failure(w, 409, "execution outcome unknown; reconcile using the request key")
		return
	}
	status := 200
	output, _ := json.Marshal(result)
	if r.URL.Path == "/mcp" {
		output = bytes.TrimSpace([]byte(result.Stdout))
		if result.ExitCode != 0 || !json.Valid(output) {
			failure(w, 502, "MCP execution failed; reconcile before retrying")
			return
		}
	}
	if path != "" {
		data, _ := json.Marshal(receipt{Hash: hash, Complete: true, Status: status, Body: output})
		if e = atomicWrite(path, data); e != nil {
			failure(w, 409, "response persistence failed; reconcile using the request key")
			return
		}
	}
	respond(w, status, output)
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicWrite(path string, data []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
