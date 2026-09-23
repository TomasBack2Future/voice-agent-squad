package execution

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Handler exposes admission only. It deliberately cannot issue authorizations or
// reconcile active runs; those operations remain on the trusted local machine.
func Handler(s *Store, token string, verify func(*http.Request, Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(token) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != "POST" || (r.URL.Path != "/v1/executions/begin" && r.URL.Path != "/v1/executions/check") || r.URL.RawQuery != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
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
				http.Error(w, "run verifier unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := verify(r, request); err != nil {
				http.Error(w, "run identity rejected", http.StatusForbidden)
				return
			}
		}
		receipt, err := s.Admit(r.Context(), request, begin)
		if err != nil {
			http.Error(w, "execution admission rejected", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
	})
}
