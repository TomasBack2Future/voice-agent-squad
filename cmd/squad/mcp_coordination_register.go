package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/mcp"
	"github.com/zsiec/squad/internal/touch"
)

// registerCoordinationTools wires the rest of squad's CLI surface into MCP:
// chat-side coordination (handoff), claim ownership (force-release,
// reassign), read-only views (history, who, status), file touches (touch,
// untouch, touches list-others), the chat archive, and the PR integration
// verbs (pr-link, pr-close). All of these used to require shelling out to
// the CLI.
func registerCoordinationTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	srv.Register(mcp.Tool{
		Name:        "squad_claim_inspect",
		Description: "Read the current repository's exact claim. Missing claim returns env_claim:null; query errors fail. This snapshot does not grant ownership.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"item_id":{"type":"string","minLength":1}},"required":["item_id"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID string `json:"item_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return inspectClaim(ctx, db, repoID, args.ItemID)
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_handoff",
		Description: "Post a handoff brief and release every claim this agent currently holds.",
		InputSchema: json.RawMessage(schemaHandoff),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Shipped              []string `json:"shipped"`
				InFlight             []string `json:"in_flight"`
				SurprisedBy          []string `json:"surprised_by"`
				Unblocks             []string `json:"unblocks"`
				Note                 string   `json:"note"`
				AgentID              string   `json:"agent_id"`
				ProposeFromSurprises bool     `json:"propose_from_surprises"`
				DryRun               bool     `json:"dry_run"`
				MaxProposals         int      `json:"max_proposals"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return Handoff(ctx, HandoffArgs{
				Chat:                 newChatService(db, repoID),
				ClaimStore:           claims.New(db, repoID, nil),
				DB:                   db,
				RepoID:               repoID,
				RepoRoot:             repoRoot,
				ItemsDir:             filepath.Join(repoRoot, ".squad", "items"),
				AgentID:              agent,
				SessionID:            os.Getenv("SQUAD_SESSION_ID"),
				Shipped:              args.Shipped,
				InFlight:             args.InFlight,
				SurprisedBy:          args.SurprisedBy,
				Unblocks:             args.Unblocks,
				Note:                 args.Note,
				ProposeFromSurprises: args.ProposeFromSurprises,
				DryRun:               args.DryRun,
				MaxProposals:         args.MaxProposals,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_force_release",
		Description: "Forcibly release a stuck claim held by another agent (requires reason).",
		InputSchema: json.RawMessage(schemaForceRelease),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string `json:"item_id"`
				Reason  string `json:"reason"`
				AgentID string `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			res, err := ForceRelease(ctx, ForceReleaseArgs{
				Store: claims.New(db, repoID, nil), ItemID: args.ItemID,
				AgentID: agent, Reason: args.Reason,
			})
			if errors.Is(err, claims.ErrReasonRequired) {
				return nil, errors.New("--reason is required for force-release")
			}
			if errors.Is(err, claims.ErrNotClaimed) {
				return nil, errors.New("no active claim on " + args.ItemID)
			}
			return res, err
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_reassign",
		Description: "Transfer the caller's claim by releasing it and pinging the new owner.",
		InputSchema: json.RawMessage(schemaReassign),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string `json:"item_id"`
				To      string `json:"to"`
				AgentID string `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return Reassign(ctx, ReassignArgs{
				Store: claims.New(db, repoID, nil), ItemID: args.ItemID,
				AgentID: agent, Target: args.To,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_archive",
		Description: "Roll old chat messages into a per-month archive DB.",
		InputSchema: json.RawMessage(schemaArchive),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Before string `json:"before"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if args.Before == "" {
				args.Before = "30d"
			}
			dur, err := parseHumanDuration(args.Before)
			if err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return Archive(ctx, ArchiveArgs{DB: db, RepoID: repoID, Before: dur})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_history",
		Description: "Return every chat message attached to an item, in time order.",
		InputSchema: json.RawMessage(schemaHistory),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID string `json:"item_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return History(ctx, HistoryArgs{
				Chat: newChatService(db, repoID), ItemID: args.ItemID,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_who",
		Description: "List registered agents with status, current claim, and last tick.",
		InputSchema: json.RawMessage(schemaWho),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ActiveOnly bool `json:"active_only"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return Who(ctx, WhoArgs{
				Chat: newChatService(db, repoID), ActiveOnly: args.ActiveOnly,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_status",
		Description: "Return claimed / ready / blocked / done counts for the current repo.",
		InputSchema: json.RawMessage(schemaStatus),
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return Status(ctx, StatusArgs{RepoRoot: repoRoot})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_touch",
		Description: "Declare files this agent is editing so peers see the overlap.",
		InputSchema: json.RawMessage(schemaTouch),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string   `json:"item_id"`
				Paths   []string `json:"paths"`
				AgentID string   `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return Touch(ctx, TouchArgs{
				Tracker: touch.New(db, repoID), AgentID: agent,
				ItemID: args.ItemID, Paths: args.Paths,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_untouch",
		Description: "Release file touches; empty paths releases all touches held by the agent.",
		InputSchema: json.RawMessage(schemaUntouch),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Paths   []string `json:"paths"`
				AgentID string   `json:"agent_id"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return Untouch(ctx, UntouchArgs{
				Tracker: touch.New(db, repoID), AgentID: agent,
				Paths: args.Paths,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_touches_list_others",
		Description: "List active file touches held by agents other than the caller.",
		InputSchema: json.RawMessage(schemaTouchesListOthers),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				AgentID string `json:"agent_id"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return TouchesListOthers(ctx, TouchesListOthersArgs{
				Tracker: touch.New(db, repoID), AgentID: agent,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_pr_link",
		Description: "Record a pending PR ↔ item mapping; returns the marker to embed in the PR body.",
		InputSchema: json.RawMessage(schemaPRLink),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID           string `json:"item_id"`
				WriteToClipboard bool   `json:"write_to_clipboard"`
				PR               int    `json:"pr"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return PRLink(ctx, PRLinkArgs{
				RepoRoot: repoRoot, ItemID: args.ItemID,
				WriteToClipboard: args.WriteToClipboard, PR: args.PR,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_pr_close",
		Description: "Find the squad item linked to a merged PR and archive it (CI-only).",
		InputSchema: json.RawMessage(schemaPRClose),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				PRNumber string `json:"pr_number"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return PRClose(ctx, PRCloseArgs{
				RepoRoot: repoRoot, PRNumber: args.PRNumber,
			})
		},
	})
}
