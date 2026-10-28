package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/identity"
	"github.com/zsiec/squad/internal/mcp"
)

func registerDispatchControllerTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	registerDispatchAdmissionTools(srv, db, repoID, repoRoot)
	srv.Register(mcp.Tool{Name: "squad_dispatch_worker_handoff_get", Description: "Read an immutable supervised Worker custody transition receipt.", InputSchema: json.RawMessage(`{"type":"object","required":["request_id"],"properties":{"request_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args map[string]string
		if err := json.Unmarshal(raw, &args); err != nil || len(args) != 1 || args["request_id"] == "" {
			return nil, fmt.Errorf("one exact Worker handoff request identity required")
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).WorkerHandoffReceipt(ctx, args["request_id"])
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_worker_handoff", Description: "Current-controller source Worker transfer with exact original-owner consent or human-authorized source recovery attestation, reconciled pin and joined native; preserves hold and excludes ENV.", InputSchema: json.RawMessage(`{"type":"object","required":["request"],"properties":{"request":{"type":"object"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var outer map[string]json.RawMessage
		if err := json.Unmarshal(raw, &outer); err != nil {
			return nil, err
		}
		if len(outer) != 1 || outer["request"] == nil {
			return nil, fmt.Errorf("one Worker handoff request required")
		}
		q, err := decodeWorkerHandoff(outer["request"])
		if err != nil {
			return nil, err
		}
		if err = workerHandoffNative(q); err != nil {
			return nil, err
		}
		if err = requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := identity.AgentID()
		if err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).WorkerHandoff(ctx, actor, q)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_controller_bind", Description: "Owner-initiated legacy native binding; does not migrate a client or change permissions.", InputSchema: json.RawMessage(`{"type":"object","required":["native_session","expected_epoch"],"properties":{"native_session":{"type":"string"},"expected_epoch":{"type":"integer","const":0}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Native string `json:"native_session"`
			Epoch  int64  `json:"expected_epoch"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := identity.AgentID()
		if err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).BindController(ctx, actor, a.Native, a.Epoch)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_controller_status", Description: "Read current legitimate controller native/epoch; no impersonation.", InputSchema: json.RawMessage(`{"type":"object","properties":{"actor":{"type":"string"},"reservation":{"type":"string"},"generation":{"type":"integer","minimum":1}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Actor      string `json:"actor"`
			ReserveKey string `json:"reservation"`
			Generation int64  `json:"generation"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		if a.Actor == "" {
			var err error
			a.Actor, err = identity.AgentID()
			if err != nil {
				return nil, err
			}
		}
		s := dispatch.New(db, repoID, nil)
		if a.ReserveKey != "" {
			return s.ControllerForReservation(ctx, a.Actor, a.ReserveKey, a.Generation)
		}
		return s.Controller(ctx, a.Actor)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_handoff", Description: "Old-owner initiated exact-inventory CAS. Transfers controller ownership and unhandled recipient routing atomically; never transfers Worker or ENV claims.", InputSchema: json.RawMessage(`{"type":"object","required":["request"],"properties":{"request":{"type":"object","required":["request_id","expected_epoch","old_native","new_actor","new_native","reservations"],"properties":{"request_id":{"type":"string"},"expected_epoch":{"type":"integer","minimum":1},"old_native":{"type":"string"},"new_actor":{"type":"string"},"new_native":{"type":"string"},"reservations":{"type":"array","minItems":1,"maxItems":1000,"items":{"type":"object"}}},"additionalProperties":false}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Request dispatch.HandoffRequest `json:"request"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := identity.AgentID()
		if err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).Handoff(ctx, actor, a.Request)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_receiver_preflight", Description: "Qualify standby-to-active receiver readiness before asynchronous launch; read-only, never enables a fallback.", InputSchema: json.RawMessage(`{"type":"object","properties":{"actor":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Actor string `json:"actor"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		if a.Actor == "" {
			var err error
			a.Actor, err = identity.AgentID()
			if err != nil {
				return nil, err
			}
		}
		return dispatch.New(db, repoID, nil).ReceiverReady(ctx, a.Actor)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_handoff_get", Description: "Read immutable successful controller handoff audit receipt.", InputSchema: json.RawMessage(`{"type":"object","required":["request_id"],"properties":{"request_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			ID string `json:"request_id"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).HandoffReceipt(ctx, a.ID)
	}})
	for _, release := range []bool{false, true} {
		name := "squad_dispatch_receiver_bind"
		if release {
			name = "squad_dispatch_receiver_release"
		}
		srv.Register(mcp.Tool{Name: name, Description: "Exact actor/native/epoch receiver custody; no lease expiry or replacement on transport failure.", InputSchema: json.RawMessage(`{"type":"object","required":["native_session","epoch","incarnation"],"properties":{"native_session":{"type":"string"},"epoch":{"type":"integer","minimum":1},"incarnation":{"type":"string"},"owner_pid":{"type":"integer","minimum":1},"wake_kind":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Native      string `json:"native_session"`
				Epoch       int64  `json:"epoch"`
				Incarnation string `json:"incarnation"`
				OwnerPID    int    `json:"owner_pid"`
				WakeKind    string `json:"wake_kind"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			actor, err := identity.AgentID()
			if err != nil {
				return nil, err
			}
			s := dispatch.New(db, repoID, nil)
			if release {
				err = s.ReleaseReceiver(ctx, actor, a.Native, a.Incarnation, a.Epoch)
			} else if a.OwnerPID > 0 || a.WakeKind != "" {
				err = s.BindReceiverReady(ctx, actor, a.Native, a.Incarnation, a.Epoch, a.OwnerPID, a.WakeKind)
			} else {
				err = s.BindReceiver(ctx, actor, a.Native, a.Incarnation, a.Epoch)
			}
			return map[string]bool{"ok": err == nil}, err
		}})
	}

}

func decodeDispatchArguments(raw json.RawMessage, value any) error {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("dispatch arguments must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one dispatch argument object")
	}
	return nil
}

func registerDispatchAdmissionTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	caller := func() (*dispatch.Store, string, error) {
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, "", err
		}
		actor, err := identity.AgentID()
		return dispatch.New(db, repoID, nil), actor, err
	}
	srv.Register(mcp.Tool{Name: "squad_dispatch_reserve", Description: "Reserve one canonical source under the calling Dispatcher identity.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation","source"],"properties":{"reservation":{"type":"string"},"source":{"type":"string"},"note":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args struct {
			Reservation string `json:"reservation"`
			Source      string `json:"source"`
			Note        string `json:"note"`
		}
		if err := decodeDispatchArguments(raw, &args); err != nil {
			return nil, err
		}
		store, actor, err := caller()
		if err != nil {
			return nil, err
		}
		return store.Reserve(ctx, args.Reservation, args.Source, actor, args.Note, 15*time.Minute)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_attach", Description: "Attach a canonical item to this caller's exact reservation generation.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation","item","generation"],"properties":{"reservation":{"type":"string"},"item":{"type":"string"},"generation":{"type":"integer","minimum":1}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args struct {
			Reservation string `json:"reservation"`
			Item        string `json:"item"`
			Generation  int64  `json:"generation"`
		}
		if err := decodeDispatchArguments(raw, &args); err != nil {
			return nil, err
		}
		store, actor, err := caller()
		if err != nil {
			return nil, err
		}
		return store.Attach(ctx, args.Reservation, args.Item, actor, args.Generation)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_bind", Description: "Bind a Worker only after the calling Dispatcher's native receiver is ready.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation","worker_session","generation"],"properties":{"reservation":{"type":"string"},"worker_session":{"type":"string"},"generation":{"type":"integer","minimum":1}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args struct {
			Reservation string `json:"reservation"`
			Worker      string `json:"worker_session"`
			Generation  int64  `json:"generation"`
		}
		if err := decodeDispatchArguments(raw, &args); err != nil {
			return nil, err
		}
		store, actor, err := caller()
		if err != nil {
			return nil, err
		}
		return store.Bind(ctx, args.Reservation, actor, args.Worker, args.Generation)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_list", Description: "Read current repository dispatch reservations.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := decodeDispatchArguments(raw, new(struct{})); err != nil {
			return nil, err
		}
		store, _, err := caller()
		if err != nil {
			return nil, err
		}
		reservations, err := store.List(ctx, false)
		return map[string]any{"reservations": reservations}, err
	}})
}
