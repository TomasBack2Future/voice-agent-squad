package execution

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Handler exposes admission only. It deliberately cannot issue authorizations or
// reconcile active runs; those operations remain on the trusted local machine.
func Handler(s *Store, token string, verify func(*http.Request, Request) error) http.Handler {
	return HandlerWithDiagnostics(s, token, verify, nil)
}

func writeClass(w http.ResponseWriter, status int, class Class) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]Class{"class": class})
}

// HandlerWithDiagnostics also serves the read-only readiness probe, which runs
// lookup with the service's real environment. Failures carry a sanitized class.
func HandlerWithDiagnostics(s *Store, token string, verify func(*http.Request, Request) error, lookup func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/executions/identity" {
			nonce := r.URL.Query().Get("nonce")
			if len(token) < 32 || len(nonce) < 16 || len(nonce) > 64 || len(r.URL.Query()) != 1 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(identityDocument{Service: ServiceName, Proof: identityProof(token, s.repoID, nonce)})
			return
		}
		if len(token) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != "POST" || (r.URL.Path != "/v1/executions/begin" && r.URL.Path != "/v1/executions/check" && r.URL.Path != "/v1/executions/readiness") || r.URL.RawQuery != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if r.URL.Path == "/v1/executions/readiness" {
			var probe struct {
				Repository string `json:"repository"`
			}
			if err := decoder.Decode(&probe); err != nil || decoder.Decode(new(any)) != io.EOF || !repository.MatchString(probe.Repository) {
				writeClass(w, http.StatusBadRequest, ClassRunMismatch)
				return
			}
			result := s.Diagnose(r.Context(), probe.Repository, lookup)
			w.Header().Set("Content-Type", "application/json")
			if !result.Ready {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		var request Request
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		begin := r.URL.Path == "/v1/executions/begin"
		if begin {
			if verify == nil {
				writeClass(w, http.StatusServiceUnavailable, ClassDiagnosticsAbsent)
				return
			}
			if err := verify(r, request); err != nil {
				// An unreachable or misconfigured verifier is an outage of this
				// authority, not a verdict on the run.
				switch c := ClassOf(err); c {
				case ClassToolMissing, ClassCredentialMissing, ClassGitHubUnavailable:
					writeClass(w, http.StatusServiceUnavailable, c)
				default:
					writeClass(w, http.StatusForbidden, ClassRunMismatch)
				}
				return
			}
		}
		receipt, err := s.Admit(r.Context(), request, begin)
		if err != nil {
			writeClass(w, http.StatusConflict, ClassAuthorizationDenied)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
	})
}
