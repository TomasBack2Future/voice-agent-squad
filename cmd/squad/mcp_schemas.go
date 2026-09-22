package main

const schemaClaim = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "intent"],
  "properties": {
    "item_id":  {"type": "string", "description": "Item identifier, e.g. BUG-001."},
    "intent":   {"type": "string", "description": "One sentence describing what you intend to ship."},
    "agent_id": {"type": "string", "description": "Caller agent identifier. Defaults to the registered agent for this session."},
    "touches":  {"type": "array", "items": {"type": "string"}, "description": "File paths the agent intends to modify."},
    "scope": {"type": "string", "description": "Registered resource service scope; omitted retains legacy coverage."},
    "long":     {"type": "boolean", "description": "Use the long-running stale-claim threshold (2h)."}
  },
  "additionalProperties": false
}`

const schemaDone = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "summary"],
  "properties": {
    "item_id":  {"type": "string"},
    "summary":  {"type": "string", "minLength": 1, "maxLength": 200},
    "agent_id": {"type": "string"},
    "force":    {"type": "boolean", "description": "Override missing evidence_required (records a manual attestation)."}
  },
  "additionalProperties": false
}`

const schemaNext = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "default": 5},
    "include_claimed": {"type": "boolean"},
    "all": {"type": "boolean"},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaSay = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["message"],
  "properties": {
    "message":  {"type": "string", "minLength": 1, "maxLength": 4000},
    "to":       {"type": "string", "description": "Item ID, 'global', or @agent. Default: caller's current claim thread."},
    "mention":  {"type": "array", "items": {"type": "string"}, "description": "Additional @-mentions."},
    "verb":     {"type": "string", "enum": ["say", "thinking", "milestone", "stuck", "fyi"], "default": "say"},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaAsk = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["target", "question"],
  "properties": {
    "target":   {"type": "string", "description": "Either an @agent or a thread name."},
    "question": {"type": "string", "minLength": 1, "maxLength": 4000},
    "to":       {"type": "string", "description": "Optional thread override; default 'global'."},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaTick = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaRegister = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["as"],
  "properties": {
    "as":   {"type": "string", "pattern": "^[a-z0-9-]{3,32}$"},
    "name": {"type": "string", "minLength": 1, "maxLength": 64},
    "capabilities": {"type": "array", "items": {"type": "string"}}
  },
  "additionalProperties": false
}`

const schemaWhoami = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const schemaRelease = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id"],
  "properties": {
    "item_id": {"type": "string"},
    "outcome": {"type": "string", "enum": ["released", "abandoned", "blocked"], "default": "released"},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaAutoRefineApply = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "new_body"],
  "properties": {
    "item_id":  {"type": "string", "description": "Identifier of a captured item to be auto-refined."},
    "new_body": {"type": "string", "description": "Drafted markdown body (Problem / Context / Acceptance criteria) that replaces the existing body. Must satisfy DoR."},
    "area":     {"type": "string", "description": "Optional. Free-form area string written to the item's frontmatter. Required when the captured item's area is the placeholder \"<fill-in>\"; the DoR area-set rule rejects placeholders."}
  },
  "additionalProperties": false
}`

const schemaBlocked = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "reason"],
  "properties": {
    "item_id": {"type": "string"},
    "reason":  {"type": "string", "minLength": 1, "maxLength": 500},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaProgress = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "note"],
  "properties": {
    "item_id": {"type": "string"},
    "note":    {"type": "string", "minLength": 1, "maxLength": 1000},
    "pct":     {"type": "integer", "minimum": 0, "maximum": 100, "description": "Optional progress percentage; defaults to 0."},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaReviewRequest = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id"],
  "properties": {
    "item_id":  {"type": "string"},
    "reviewer": {"type": "string", "description": "Reviewer agent (single)."},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaDoctor = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const schemaReady = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "ids":     {"type": "array", "items": {"type": "string"}, "description": "Item IDs to lint. Empty means all captured items."},
    "promote": {"type": "boolean", "description": "Promote items that pass DoR to status=open. Only items in status=captured are eligible; non-captured items are reported with a skip_reason. Default: true.", "default": true},
    "agent_id": {"type": "string", "description": "Caller agent identifier; recorded as accepted_by on promoted items."}
  },
  "additionalProperties": false
}`

const schemaStats = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "window_seconds": {"type": "integer", "minimum": 0, "description": "Metric window in seconds. 0 = unbounded; default 86400 (24h)."}
  },
  "additionalProperties": false
}`

const schemaStandup = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "since":         {"type": "integer", "minimum": 0, "description": "Unix timestamp lower bound. Defaults to now - 24h."},
    "agent_id":      {"type": "string", "description": "Caller agent identifier; the report scopes to this agent."}
  },
  "additionalProperties": false
}`

const schemaAnalyze = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["epic_name"],
  "properties": {
    "epic_name": {"type": "string", "minLength": 1, "description": "Epic name (matches the file under .squad/epics/<name>.md, no extension)."},
    "agent_id":  {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaListItems = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "status":   {"type": "string", "enum": ["captured", "open", "in_progress", "blocked", "done"], "description": "Lifecycle state. captured=in inbox awaiting accept; open=accepted/claimable; in_progress=claimed; blocked=needs unblock; done=completed."},
    "type":     {"type": "string", "description": "Item type, lower-case (e.g. bug, feature, task, chore, tech-debt). The set is config-driven via id_prefixes, so any string is accepted."},
    "priority": {"type": "string", "enum": ["P0", "P1", "P2", "P3"]},
    "agent":    {"type": "string", "description": "Filter to items currently claimed by this agent."},
    "limit":    {"type": "integer", "minimum": 1, "maximum": 200, "default": 50}
  },
  "additionalProperties": false
}`

const schemaGetItem = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id"],
  "properties": {
    "item_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaAttest = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "kind"],
  "properties": {
    "item_id":        {"type": "string"},
    "kind":           {"type": "string", "enum": ["test", "lint", "typecheck", "build", "review", "manual"]},
    "work_dir": {"type": "string", "description": "Absolute command execution directory; evidence stays in the selected ledger."},
    "command":        {"type": "string", "description": "Shell command to run and capture (required for non-review kinds)."},
    "findings_file":  {"type": "string", "description": "Review findings file (kind=review only)."},
    "reviewer_agent": {"type": "string", "description": "Reviewer agent id (kind=review only)."},
    "agent_id":       {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaAttestations = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id"],
  "properties": {
    "item_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaLearningPropose = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["kind", "slug", "title", "area"],
  "properties": {
    "kind":       {"type": "string", "enum": ["gotcha", "pattern", "dead-end"]},
    "slug":       {"type": "string", "pattern": "^[a-z0-9-]{1,64}$"},
    "title":      {"type": "string", "minLength": 1, "maxLength": 200},
    "area":       {"type": "string", "minLength": 1, "maxLength": 64},
    "paths":      {"type": "array", "items": {"type": "string"}},
    "session_id": {"type": "string"},
    "agent_id":   {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaLearningQuick = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["one_liner"],
  "properties": {
    "one_liner":  {"type": "string", "minLength": 1, "maxLength": 200},
    "kind":       {"type": "string", "enum": ["gotcha", "pattern", "dead-end"]},
    "session_id": {"type": "string"},
    "agent_id":   {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaLearningList = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "area":  {"type": "string"},
    "state": {"type": "string", "enum": ["proposed", "approved", "rejected"]},
    "kind":  {"type": "string", "enum": ["gotcha", "pattern", "dead-end"]}
  },
  "additionalProperties": false
}`

const schemaLearningApprove = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["slug"],
  "properties": {
    "slug": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaLearningReject = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["slug", "reason"],
  "properties": {
    "slug":   {"type": "string"},
    "reason": {"type": "string", "minLength": 1, "maxLength": 1000}
  },
  "additionalProperties": false
}`

const schemaLearningAgentsMdSuggest = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["diff_path", "rationale"],
  "properties": {
    "diff_path": {"type": "string", "description": "Path to a unified-diff file (use 'git diff -- AGENTS.md' to produce)."},
    "rationale": {"type": "string", "minLength": 1, "maxLength": 2000},
    "slug":      {"type": "string"},
    "agent_id":  {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaLearningAgentsMdApprove = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {"type": "string", "description": "Proposal id (timestamp-slug filename, e.g. 20260425T103000Z-foo)."}
  },
  "additionalProperties": false
}`

const schemaLearningAgentsMdReject = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id", "reason"],
  "properties": {
    "id":     {"type": "string"},
    "reason": {"type": "string", "minLength": 1, "maxLength": 1000}
  },
  "additionalProperties": false
}`

const schemaHandoff = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "shipped":                {"type": "array", "items": {"type": "string"}},
    "in_flight":              {"type": "array", "items": {"type": "string"}},
    "surprised_by":           {"type": "array", "items": {"type": "string"}},
    "unblocks":               {"type": "array", "items": {"type": "string"}},
    "note":                   {"type": "string"},
    "agent_id":               {"type": "string"},
    "propose_from_surprises": {"type": "boolean", "description": "Auto-draft a learning proposal per surprise (explicit list, else mined from chat history)."},
    "dry_run":                {"type": "boolean"},
    "max_proposals":          {"type": "integer", "minimum": 1, "maximum": 50}
  },
  "additionalProperties": false
}`

const schemaForceRelease = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "reason"],
  "properties": {
    "item_id":  {"type": "string"},
    "reason":   {"type": "string", "minLength": 1, "description": "Why the claim is being forcibly released."},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaReassign = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "to"],
  "properties": {
    "item_id":  {"type": "string"},
    "to":       {"type": "string", "description": "Target agent id (with or without leading @)."},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaArchive = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "before": {"type": "string", "description": "Cut-off duration; default 30d. Accepts Go-style (720h) or human (30d).", "default": "30d"}
  },
  "additionalProperties": false
}`

const schemaHistory = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id"],
  "properties": {
    "item_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaWho = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "active_only": {"type": "boolean", "description": "Hide stale/offline agents."}
  },
  "additionalProperties": false
}`

const schemaStatus = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const schemaTouch = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id", "paths"],
  "properties": {
    "item_id":  {"type": "string"},
    "paths":    {"type": "array", "items": {"type": "string"}, "minItems": 1},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaUntouch = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "paths":    {"type": "array", "items": {"type": "string"}, "description": "If empty, releases all touches held by the caller."},
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaTouchesListOthers = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaPRLink = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item_id"],
  "properties": {
    "item_id":            {"type": "string"},
    "write_to_clipboard": {"type": "boolean"},
    "pr":                 {"type": "integer", "minimum": 1, "description": "If set, append the marker to an existing PR via gh pr edit."}
  },
  "additionalProperties": false
}`

const schemaNew = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["type", "title"],
  "properties": {
    "type":     {"type": "string", "enum": ["bug", "feat", "feature", "task", "chore", "debt", "tech-debt", "bet"]},
    "title":    {"type": "string", "minLength": 1, "maxLength": 200},
    "priority": {"type": "string", "enum": ["P0", "P1", "P2", "P3"]},
    "area":     {"type": "string"},
    "estimate": {"type": "string", "description": "Duration like 30m, 1h, 4h, 1d."},
    "risk":     {"type": "string", "enum": ["low", "medium", "high"]},
    "ready":    {"type": "boolean", "description": "Skip the captured/inbox state and file as immediately claimable."}
  },
  "additionalProperties": false
}`

const schemaAccept = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["ids"],
  "properties": {
    "ids": {"type": "array", "items": {"type": "string"}, "minItems": 1}
  },
  "additionalProperties": false
}`

const schemaReject = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["ids", "reason"],
  "properties": {
    "ids":    {"type": "array", "items": {"type": "string"}, "minItems": 1},
    "reason": {"type": "string", "minLength": 1}
  },
  "additionalProperties": false
}`

const schemaInbox = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "mine":        {"type": "boolean"},
    "ready_only":  {"type": "boolean"},
    "parent_spec": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaDecompose = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["spec_name"],
  "properties": {
    "spec_name": {"type": "string", "minLength": 1}
  },
  "additionalProperties": false
}`

const schemaPRClose = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["pr_number"],
  "properties": {
    "pr_number": {"type": "string", "description": "PR number as a string (CI-friendly)."}
  },
  "additionalProperties": false
}`

const schemaIntakeOpen = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["mode"],
  "properties": {
    "mode":          {"type": "string", "enum": ["new", "refine"]},
    "idea_seed":     {"type": "string", "description": "Free-form starting idea for new mode."},
    "refine_item_id": {"type": "string", "description": "Required for refine mode; targets an existing captured item."}
  },
  "additionalProperties": false
}`

const schemaIntakeTurn = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["session_id", "role", "content"],
  "properties": {
    "session_id":    {"type": "string"},
    "role":          {"type": "string", "enum": ["user", "agent", "system"]},
    "content":       {"type": "string", "minLength": 1},
    "fields_filled": {"type": "array", "items": {"type": "string"}, "description": "Honor-system claim about which checklist fields this turn satisfied."}
  },
  "additionalProperties": false
}`

const schemaIntakeStatus = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["session_id"],
  "properties": {
    "session_id": {"type": "string"}
  },
  "additionalProperties": false
}`

const schemaIntakeCommit = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["session_id", "bundle"],
  "properties": {
    "session_id": {"type": "string"},
    "bundle":     {"type": "object", "description": "Bundle: spec/epics/items per intake.Bundle shape."},
    "ready":      {"type": "boolean", "description": "If true, items land as open instead of captured."}
  },
  "additionalProperties": false
}`

const schemaResourcesDefine = `{
 "$schema":"https://json-schema.org/draft/2020-12/schema",
 "type":"object","required":["definitions"],"additionalProperties":false,
 "properties":{"definitions":{"type":"array","minItems":1,"items":{
 "type":"object","additionalProperties":false,
 "required":["item_id","group","default_scope","service_scope"],
 "properties":{"item_id":{"type":"string"},"group":{"type":"string"},"default_scope":{"type":"string"},"service_scope":{"type":"string"}}
 }}}
}`
