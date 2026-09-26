package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/config"
	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/identity"
	"github.com/zsiec/squad/internal/items"
	"github.com/zsiec/squad/internal/mcp"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/terminalevents"
)

// errNoRepo is returned by per-repo handlers when MCP was started outside a
// squad repo (so repoRoot/repoID are empty). The CLI surfaces this as a
// structured RPC error rather than crashing init.
var errNoRepo = errors.New("squad mcp: no repo discovered (run from inside a squad repo, or pass repo_root)")

// asInvalidParams wraps err so the MCP server returns code -32602
// (Invalid params) instead of the catch-all -32603 (Internal error).
// The "no repo" case is the canonical example: the call's params imply a
// context (a repo) that doesn't exist, which is precisely what -32602 covers.
func asInvalidParams(err error) error {
	if err == nil {
		return nil
	}
	return &mcp.ToolError{Code: mcp.CodeInvalidParams, Err: err}
}

type registerEnvelope struct {
	*RegisterResult
	Warnings []string `json:"warnings,omitempty"`
}

func registerTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	registerExecutionTools(srv, db, repoID, repoRoot)
	registerLifecycleTools(srv, db, repoID, repoRoot)
	registerIntakeTools(srv, db, repoID, repoRoot)
	registerIntakeInterviewTools(srv, db, repoID, repoRoot)
	registerChatTools(srv, db, repoID, repoRoot)
	registerInspectionTools(srv, db, repoID, repoRoot)
	registerEvidenceTools(srv, db, repoID, repoRoot)
	registerLearningTools(srv, repoRoot)
	registerCoordinationTools(srv, db, repoID, repoRoot)
}

func resolveAgentID(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return identity.AgentID()
}

func requireRepo(repoRoot, repoID string) error {
	if repoRoot == "" || repoID == "" {
		return asInvalidParams(errNoRepo)
	}
	return nil
}

func itemsDirOf(repoRoot string) string { return filepath.Join(repoRoot, ".squad", "items") }
func doneDirOf(repoRoot string) string  { return filepath.Join(repoRoot, ".squad", "done") }
func attDirOf(repoRoot string) string   { return filepath.Join(repoRoot, ".squad", "attestations") }

func registerLifecycleTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	srv.Register(mcp.Tool{
		Name: "squad_heartbeat", Description: "Renew owned active claims under the exact dispatched Worker generation; does not consume messages.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"agent_id":{"type":"string"},"reservation":{"type":"string"},"worker_session":{"type":"string"},"generation":{"type":"integer","minimum":1},"check":{"type":"boolean"}},"required":["reservation","worker_session","generation"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Agent       string `json:"agent_id"`
				Reservation string `json:"reservation"`
				Session     string `json:"worker_session"`
				Generation  int64  `json:"generation"`
				Check       bool   `json:"check"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			agent, err := resolveAgentID(a.Agent)
			if err != nil {
				return nil, err
			}
			err = claims.New(db, repoID, nil).WorkerHeartbeat(ctx, agent, a.Reservation, a.Session, a.Generation, a.Check)
			return map[string]bool{"ok": err == nil}, err
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_register",
		Description: "Register this agent in the squad global database.",
		InputSchema: json.RawMessage(schemaRegister),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				As           string    `json:"as"`
				Name         string    `json:"name"`
				Capabilities *[]string `json:"capabilities"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			ra := RegisterArgs{As: args.As, Name: args.Name}
			if args.Capabilities != nil {
				ra.Capabilities = *args.Capabilities
				ra.SetCapabilities = true
			}
			res, warnings, err := Register(ctx, ra)
			if err != nil {
				return nil, err
			}
			return registerEnvelope{RegisterResult: res, Warnings: warnings}, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_whoami",
		Description: "Return the agent id this session resolves to, plus any current claim in this repo.",
		InputSchema: json.RawMessage(schemaWhoami),
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			// Scope claim lookup to the caller's discovered repo. Empty
			// repoID (MCP spawned outside a squad repo) means no claim is
			// reported — preventing cross-repo state from bleeding into
			// the response.
			return Whoami(ctx, WhoamiArgs{DB: db, RepoID: repoID})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_next",
		Description: "List ready items in priority order (excludes items already claimed).",
		InputSchema: json.RawMessage(schemaNext),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Limit          int    `json:"limit"`
				IncludeClaimed bool   `json:"include_claimed"`
				All            bool   `json:"all"`
				AgentID        string `json:"agent_id"`
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
			res, err := NextItem(ctx, NextArgs{
				ItemsDir:       itemsDirOf(repoRoot),
				DoneDir:        doneDirOf(repoRoot),
				DB:             db,
				RepoID:         repoID,
				AgentID:        agent,
				Limit:          args.Limit,
				IncludeClaimed: args.IncludeClaimed,
				All:            args.All,
			})
			if errors.Is(err, ErrNoReadyItems) {
				return NextResult{Items: []NextRow{}, Total: 0}, nil
			}
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_resources_check",
		Description: "Read-only installed ENV item/scope admission. Reports contention without taking a claim.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"item":{"type":"string"},"scope":{"type":"string"},"require_policy":{"type":"boolean"}},"required":["item"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Item          string `json:"item"`
				Scope         string `json:"scope"`
				RequirePolicy bool   `json:"require_policy"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return claims.New(db, repoID, nil).CheckResource(ctx, a.Item, a.Scope, filepath.Join(repoRoot, ".squad/items"), filepath.Join(repoRoot, ".squad/done"), a.RequirePolicy)
		},
	})

	srv.Register(mcp.Tool{
		Name: "squad_dispatch_continue", Description: "Dispatcher-owned fenced continuation for the same Worker; old item released/done, new item held by same actor.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reservation":{"type":"string"},"from_item":{"type":"string"},"item":{"type":"string"},"worker_session":{"type":"string"},"generation":{"type":"integer","minimum":1}},"required":["reservation","from_item","item","worker_session","generation"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Reservation string `json:"reservation"`
				From        string `json:"from_item"`
				Item        string `json:"item"`
				Worker      string `json:"worker_session"`
				Generation  int64  `json:"generation"`
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
			if findItemPath(filepath.Join(repoRoot, ".squad/done"), a.From) == "" || findItemPath(filepath.Join(repoRoot, ".squad/items"), a.Item) == "" {
				return nil, fmt.Errorf("original must be done and continuation must exist in live items")
			}
			return dispatch.New(db, repoID, nil).Continue(ctx, a.Reservation, actor, a.From, a.Item, a.Worker, a.Generation)
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_resources_define",
		Description: "Register ledger-owned environment resource scopes atomically. Requires idle affected claims and waiters; does not migrate active ownership.",
		InputSchema: json.RawMessage(schemaResourcesDefine),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Definitions []claims.ResourceDefinition `json:"definitions"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			if err := claims.New(db, repoID, nil).DefineResources(ctx, args.Definitions); err != nil {
				return nil, err
			}
			return map[string]int{"registered": len(args.Definitions)}, nil
		},
	})
	srv.Register(mcp.Tool{
		Name:        "squad_claim",
		Description: "Atomically claim an item by ID for the current agent. Fails if another agent holds it.",
		InputSchema: json.RawMessage(schemaClaim),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string   `json:"item_id"`
				Scope   string   `json:"scope"`
				Intent  string   `json:"intent"`
				AgentID string   `json:"agent_id"`
				Touches []string `json:"touches"`
				Long    bool     `json:"long"`
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
			res, err := Claim(ctx, ClaimArgs{
				DB:             db,
				RepoID:         repoID,
				AgentID:        agent,
				ItemID:         args.ItemID,
				Intent:         args.Intent,
				Scope:          args.Scope,
				Touches:        args.Touches,
				Long:           args.Long,
				ItemsDir:       itemsDirOf(repoRoot),
				DoneDir:        doneDirOf(repoRoot),
				ConcurrencyCap: claimConcurrencyCap(),
			})
			if err != nil {
				return nil, err
			}
			if itemPath := findItemPath(itemsDirOf(repoRoot), args.ItemID); itemPath != "" {
				if parsed, perr := items.Parse(itemPath); perr == nil {
					if t := cadenceNudgeText("claim", ""); t != "" {
						res.Tips = append(res.Tips, t)
					}
					if t := secondOpinionNudgeText(parsed.Priority, parsed.Risk); t != "" {
						res.Tips = append(res.Tips, t)
					}
					if t := milestoneTargetNudgeText(items.CountAC(parsed.Body)); t != "" {
						res.Tips = append(res.Tips, t)
					}
				}
			}
			return res, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_release",
		Description: "Release the caller's claim on an item.",
		InputSchema: json.RawMessage(schemaRelease),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string `json:"item_id"`
				Outcome string `json:"outcome"`
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
			return Release(ctx, ReleaseArgs{
				DB: db, RepoID: repoID, AgentID: agent,
				ItemID: args.ItemID, Outcome: args.Outcome,
				NotifyWaiters: func(ctx context.Context, itemID string) {
					notifyClaimWaiters(ctx, notify.NewRegistry(db), repoID, itemID)
				},
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_auto_refine_apply",
		Description: "Apply an auto-refined body to a captured item: replace the body, stamp auto_refined_at/auto_refined_by. Refuses non-captured items and bodies that fail DoR. Intended for the auto-refine subprocess flow.",
		InputSchema: json.RawMessage(schemaAutoRefineApply),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string `json:"item_id"`
				NewBody string `json:"new_body"`
				Area    string `json:"area"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, &mcp.ToolError{Code: mcp.CodeInvalidParams, Err: err}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			res, err := AutoRefineApply(ctx, AutoRefineApplyArgs{
				SquadDir: filepath.Join(repoRoot, ".squad"),
				ItemID:   args.ItemID,
				NewBody:  args.NewBody,
				Area:     args.Area,
			})
			if err != nil {
				return nil, &mcp.ToolError{Code: mcp.CodeInvalidParams, Err: err}
			}
			return res, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_done",
		Description: "Mark an item done: release claim, rewrite frontmatter, move to .squad/done/.",
		InputSchema: json.RawMessage(schemaDone),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string `json:"item_id"`
				Summary string `json:"summary"`
				AgentID string `json:"agent_id"`
				Force   bool   `json:"force"`
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
			cfg, _ := config.Load(repoRoot)
			res, err := Done(ctx, DoneArgs{
				DB: db, RepoID: repoID, AgentID: agent,
				ItemID:                  args.ItemID,
				Summary:                 args.Summary,
				ItemsDir:                itemsDirOf(repoRoot),
				DoneDir:                 doneDirOf(repoRoot),
				RepoRoot:                repoRoot,
				Force:                   args.Force,
				DefaultEvidenceRequired: cfg.Defaults.EvidenceRequired,
			})
			if err != nil {
				return nil, err
			}
			if donePath := findItemPath(doneDirOf(repoRoot), args.ItemID); donePath != "" {
				if parsed, perr := items.Parse(donePath); perr == nil {
					if t := cadenceNudgeText("done", parsed.Type); t != "" {
						res.Tips = append(res.Tips, t)
					}
				}
			}
			return res, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_blocked",
		Description: "Mark item blocked: release claim, set status: blocked, ensure ## Blocker section.",
		InputSchema: json.RawMessage(schemaBlocked),
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
			return Blocked(ctx, BlockedArgs{
				DB: db, RepoID: repoID, AgentID: agent,
				ItemID:   args.ItemID,
				Reason:   args.Reason,
				ItemsDir: itemsDirOf(repoRoot),
			})
		},
	})
}

func registerChatTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	srv.Register(mcp.Tool{
		Name:        "squad_say",
		Description: "Post a message to the current claim's thread, the global thread, or a specific item thread.",
		InputSchema: json.RawMessage(schemaSay),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Message string   `json:"message"`
				To      string   `json:"to"`
				Mention []string `json:"mention"`
				Verb    string   `json:"verb"`
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
			c := newChatService(db, repoID)
			to, err := c.ResolveThread(ctx, agent, args.To)
			if err != nil {
				return nil, err
			}
			return Say(ctx, SayArgs{
				Chat:     c,
				AgentID:  agent,
				To:       to,
				Body:     args.Message,
				Mentions: args.Mention,
				Verb:     args.Verb,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_ask",
		Description: "Ask a question of a specific agent or thread.",
		InputSchema: json.RawMessage(schemaAsk),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Target   string `json:"target"`
				Question string `json:"question"`
				To       string `json:"to"`
				AgentID  string `json:"agent_id"`
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
			c := newChatService(db, repoID)
			return Ask(ctx, AskArgs{
				Chat:     c,
				AgentID:  agent,
				To:       args.To,
				Target:   args.Target,
				Question: args.Question,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_tick",
		Description: "Show new messages since the last tick and advance the read cursor.",
		InputSchema: json.RawMessage(schemaTick),
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
			return Tick(ctx, TickArgs{Chat: newChatService(db, repoID), AgentID: agent})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_progress",
		Description: "Report progress on a held item.",
		InputSchema: json.RawMessage(schemaProgress),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID  string `json:"item_id"`
				Note    string `json:"note"`
				Pct     int    `json:"pct"`
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
			return Progress(ctx, ProgressArgs{
				DB: db, RepoID: repoID,
				Chat:    newChatService(db, repoID),
				AgentID: agent,
				ItemID:  args.ItemID,
				Pct:     args.Pct,
				Note:    args.Note,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_review_request",
		Description: "Request review on an item, optionally mentioning a reviewer.",
		InputSchema: json.RawMessage(schemaReviewRequest),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID   string `json:"item_id"`
				Reviewer string `json:"reviewer"`
				AgentID  string `json:"agent_id"`
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
			return ReviewRequest(ctx, ReviewRequestArgs{
				Chat:     newChatService(db, repoID),
				AgentID:  agent,
				ItemID:   args.ItemID,
				Reviewer: args.Reviewer,
			})
		},
	})
}

func registerInspectionTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	srv.Register(mcp.Tool{
		Name:        "squad_doctor",
		Description: "Run the hygiene sweep (stale claims, ghost agents, orphan touches, broken refs, integrity).",
		InputSchema: json.RawMessage(schemaDoctor),
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return Doctor(ctx, DoctorArgs{DB: db, RepoID: repoID, RepoRoot: repoRoot})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_standup",
		Description: "Per-agent activity rollup over a recent window: closed claims, lost claims, current open claim, stuck signals, active touches.",
		InputSchema: json.RawMessage(schemaStandup),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Since   *int64 `json:"since"`
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
			window := 24 * time.Hour
			if args.Since != nil {
				delta := time.Now().Unix() - *args.Since
				if delta > 0 {
					window = time.Duration(delta) * time.Second
				}
			}
			return Standup(ctx, StandupArgs{
				DB: db, RepoID: repoID, AgentID: agent, Window: window,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_stats",
		Description: "Operational statistics: items, claims, verification rate, claim p50/p99, WIP violations, learning-derived metrics.",
		InputSchema: json.RawMessage(schemaStats),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				WindowSeconds *int64 `json:"window_seconds"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			window := 24 * time.Hour
			if args.WindowSeconds != nil {
				window = time.Duration(*args.WindowSeconds) * time.Second
			}
			return Stats(ctx, StatsArgs{DB: db, RepoID: repoID, Window: window})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_analyze",
		Description: "Decompose an epic's items into a parallel-stream graph: streams (grouped item ids + file globs), dependency edges, parallelism factor.",
		InputSchema: json.RawMessage(schemaAnalyze),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				EpicName string `json:"epic_name"`
				AgentID  string `json:"agent_id"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			return Analyze(ctx, AnalyzeArgs{
				SquadDir: filepath.Join(repoRoot, ".squad"),
				EpicName: args.EpicName,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_list_items",
		Description: "List items filtered by status/type/priority/agent.",
		InputSchema: json.RawMessage(schemaListItems),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Status   string `json:"status"`
				Type     string `json:"type"`
				Priority string `json:"priority"`
				Agent    string `json:"agent"`
				Limit    int    `json:"limit"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			res, err := ListItems(ctx, ListItemsArgs{
				ItemsDir: itemsDirOf(repoRoot),
				DoneDir:  doneDirOf(repoRoot),
				DB:       db,
				RepoID:   repoID,
				Status:   args.Status,
				Type:     args.Type,
				Priority: args.Priority,
				Agent:    args.Agent,
				Limit:    args.Limit,
			})
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_get_item",
		Description: "Return the full item record (frontmatter + body + acceptance criteria).",
		InputSchema: json.RawMessage(schemaGetItem),
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
			return GetItem(ctx, GetItemArgs{
				ItemsDir: itemsDirOf(repoRoot),
				ItemID:   args.ItemID,
			})
		},
	})
}

func registerEvidenceTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	registerDecisionTools(srv, db, repoID, repoRoot)
	srv.Register(mcp.Tool{Name: "squad_terminal_events_publish", Description: "Publish a fenced outcome or design decision; recipient comes from the ledger. Persistence is pending, not delivered.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation","generation","worker_session","kind","outcome_id"],"properties":{"reservation":{"type":"string"},"generation":{"type":"integer","minimum":1},"worker_session":{"type":"string"},"kind":{"enum":["issue-closed","handoff-complete","blocked","decision-request","decision-resolved","reconcile-needed"]},"expected_decision":{"type":"integer","minimum":0},"outcome_id":{"type":"integer","minimum":1},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			terminalevents.PublishRequest
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := resolveAgentID(a.AgentID)
		if err != nil {
			return nil, err
		}
		id, err := (terminalevents.Store{DB: db, Repo: repoID}).Publish(ctx, actor, a.PublishRequest)
		if err != nil {
			return nil, err
		}
		return map[string]string{"event_id": id, "state": "pending"}, nil
	}})

	srv.Register(mcp.Tool{Name: "squad_terminal_events_ack", Description: "Acknowledge one delivered terminal event after recipient reconciliation.", InputSchema: json.RawMessage(`{"type":"object","required":["event_id","note"],"properties":{"event_id":{"type":"string"},"note":{"type":"string"},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			EventID string `json:"event_id"`
			Note    string `json:"note"`
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := resolveAgentID(a.AgentID)
		if err != nil {
			return nil, err
		}
		if err = (terminalevents.Store{DB: db, Repo: repoID, Recipient: actor}).Ack(ctx, a.EventID, a.Note); err != nil {
			return nil, err
		}
		return map[string]string{"event_id": a.EventID, "state": "processed"}, nil
	}})

	srv.Register(mcp.Tool{
		Name:        "squad_attest",
		Description: "Record a verification artifact (test/lint/build/typecheck/review/manual) into the evidence ledger.",
		InputSchema: json.RawMessage(schemaAttest),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ItemID        string   `json:"item_id"`
				Kind          string   `json:"kind"`
				Command       string   `json:"command"`
				Argv          []string `json:"argv"`
				WorkDir       string   `json:"work_dir"`
				FindingsFile  string   `json:"findings_file"`
				ReviewerAgent string   `json:"reviewer_agent"`
				AgentID       string   `json:"agent_id"`
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
			return Attest(ctx, AttestArgs{
				DB: db, RepoID: repoID, AgentID: agent,
				ItemID:        args.ItemID,
				Kind:          args.Kind,
				Command:       args.Command,
				Argv:          args.Argv,
				WorkDir:       args.WorkDir,
				FindingsFile:  args.FindingsFile,
				ReviewerAgent: args.ReviewerAgent,
				AttDir:        attDirOf(repoRoot),
				RepoRoot:      repoRoot,
			})
		},
	})

	srv.Register(mcp.Tool{Name: "squad_attest_revoke", Description: "Append a repo-scoped correction excluding evidence from gates; preserve the original.", InputSchema: json.RawMessage(schemaAttestRevoke), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var args AttestRevokeArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := resolveAgentID(args.AgentID)
		if err != nil {
			return nil, err
		}
		args.DB, args.RepoID, args.AgentID = db, repoID, actor
		return AttestRevoke(ctx, args)
	}})

	srv.Register(mcp.Tool{
		Name:        "squad_attestations",
		Description: "List recorded attestations for an item.",
		InputSchema: json.RawMessage(schemaAttestations),
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
			return Attestations(ctx, AttestationsArgs{
				DB: db, RepoID: repoID, ItemID: args.ItemID,
			})
		},
	})
}

func registerLearningTools(srv *mcp.Server, repoRoot string) {
	srv.Register(mcp.Tool{
		Name:        "squad_learning_propose",
		Description: "Stub a new learning artifact under .squad/learnings/<kind>s/proposed/.",
		InputSchema: json.RawMessage(schemaLearningPropose),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Kind      string   `json:"kind"`
				Slug      string   `json:"slug"`
				Title     string   `json:"title"`
				Area      string   `json:"area"`
				Paths     []string `json:"paths"`
				SessionID string   `json:"session_id"`
				AgentID   string   `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return LearningPropose(ctx, LearningProposeArgs{
				RepoRoot:  repoRoot,
				Kind:      args.Kind,
				Slug:      args.Slug,
				Title:     args.Title,
				Area:      args.Area,
				SessionID: args.SessionID,
				Paths:     args.Paths,
				CreatedBy: agent,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_quick",
		Description: "Frictionless one-line learning capture (auto-derives slug, defaults kind=gotcha, infers area).",
		InputSchema: json.RawMessage(schemaLearningQuick),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				OneLiner  string `json:"one_liner"`
				Kind      string `json:"kind"`
				SessionID string `json:"session_id"`
				AgentID   string `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return LearningQuick(ctx, LearningQuickArgs{
				RepoRoot:  repoRoot,
				OneLiner:  args.OneLiner,
				Kind:      args.Kind,
				SessionID: args.SessionID,
				CreatedBy: agent,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_list",
		Description: "List learning artifacts (filterable by area, state, kind).",
		InputSchema: json.RawMessage(schemaLearningList),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Area  string `json:"area"`
				State string `json:"state"`
				Kind  string `json:"kind"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			return LearningList(ctx, LearningListArgs{
				RepoRoot: repoRoot,
				Area:     args.Area,
				State:    args.State,
				Kind:     args.Kind,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_approve",
		Description: "Promote a proposed learning to approved/.",
		InputSchema: json.RawMessage(schemaLearningApprove),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			return LearningApprove(ctx, LearningApproveArgs{RepoRoot: repoRoot, Slug: args.Slug})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_reject",
		Description: "Archive a proposed learning under rejected/ with a reason.",
		InputSchema: json.RawMessage(schemaLearningReject),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				Slug   string `json:"slug"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			return LearningReject(ctx, LearningRejectArgs{
				RepoRoot: repoRoot, Slug: args.Slug, Reason: args.Reason,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_agents_md_suggest",
		Description: "Propose a unified-diff change to AGENTS.md (human applies via agents-md-approve).",
		InputSchema: json.RawMessage(schemaLearningAgentsMdSuggest),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				DiffPath  string `json:"diff_path"`
				Rationale string `json:"rationale"`
				Slug      string `json:"slug"`
				AgentID   string `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			agent, err := resolveAgentID(args.AgentID)
			if err != nil {
				return nil, err
			}
			return LearningAgentsMdSuggest(ctx, LearningAgentsMdSuggestArgs{
				RepoRoot:  repoRoot,
				DiffPath:  args.DiffPath,
				Rationale: args.Rationale,
				Slug:      args.Slug,
				CreatedBy: agent,
			})
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_agents_md_approve",
		Description: "Apply a proposed AGENTS.md diff via `git apply`; on success, archive the proposal.",
		InputSchema: json.RawMessage(schemaLearningAgentsMdApprove),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			res, err := LearningAgentsMdApprove(ctx, LearningAgentsMdApproveArgs{
				RepoRoot: repoRoot, ID: args.ID,
			})
			if err != nil {
				var af *ApplyFailedError
				if errors.As(err, &af) {
					return nil, fmt.Errorf("git apply failed: %s", af.Stderr)
				}
				return nil, err
			}
			return res, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "squad_learning_agents_md_reject",
		Description: "Archive a proposed AGENTS.md change under rejected/ with a reason.",
		InputSchema: json.RawMessage(schemaLearningAgentsMdReject),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ID     string `json:"id"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if repoRoot == "" {
				return nil, asInvalidParams(errNoRepo)
			}
			return LearningAgentsMdReject(ctx, LearningAgentsMdRejectArgs{
				RepoRoot: repoRoot, ID: args.ID, Reason: args.Reason,
			})
		},
	})
}
