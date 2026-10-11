package dispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// LegacyNativeAdmission binds the current controller's original kernel identity and installed
// hook in its held decision. The native owner cannot select a convenient child.
type LegacyNativeAdmission struct {
	Schema          string             `json:"schema_version"`
	RequestSHA256   string             `json:"request_sha256"`
	AuthoritySHA256 string             `json:"human_authority_sha256"`
	Client          LegacyProcess      `json:"client"`
	Lease           LegacyProcess      `json:"lease"`
	Hook            LegacyHookIdentity `json:"hook"`
}

type LegacyHookIdentity struct {
	SettingsPath     string `json:"settings_path"`
	SettingsSHA256   string `json:"settings_sha256"`
	ConfigPath       string `json:"config_path"`
	ConfigSHA256     string `json:"config_sha256"`
	ModulePath       string `json:"module_path"`
	ModuleSHA256     string `json:"module_sha256"`
	ReceiverSHA256   string `json:"receiver_sha256"`
	PythonExecutable string `json:"python_executable"`
	PythonSHA256     string `json:"python_sha256"`
}

type nativeHookObservation struct {
	Client            LegacyProcess      `json:"client"`
	Hook              LegacyHookIdentity `json:"hook"`
	DecisionRevision  int64              `json:"decision_revision"`
	DecisionOutcomeID int64              `json:"decision_outcome_id"`
	RequestSHA256     string             `json:"request_sha256,omitempty"`
}

func readLegacyFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute native hook artifact required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errors.New("native hook artifact traverses an unqualified symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("native hook artifact exceeds its regular-file bound")
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

var legacyShellSafe = regexp.MustCompile(`^[a-zA-Z0-9_/@%+=:,.-]+$`)

func legacyShellQuote(v string) string {
	if legacyShellSafe.MatchString(v) {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
}

// Inspect only the settings selected by the actual native argv, with an exact
// synchronous wildcard hook. File presence is necessary, never readiness.
func legacyHookIdentity(ctx context.Context, client int, actor, native, module, config, python string) (LegacyHookIdentity, error) {
	var out LegacyHookIdentity
	args, err := legacyProcessArguments(client)
	if err != nil {
		return out, err
	}
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "--settings" {
			if out.SettingsPath != "" {
				return out, errors.New("ambiguous original native settings")
			}
			out.SettingsPath = args[i+1]
		}
	}
	return legacyHookFiles(out.SettingsPath, actor, native, module, config, python)
}

func legacyHookFiles(settingsPath, actor, native, module, config, python string) (LegacyHookIdentity, error) {
	out := LegacyHookIdentity{SettingsPath: settingsPath}
	settings, err := readLegacyFile(out.SettingsPath, 65536)
	if err != nil {
		return out, errors.New("actual original native settings unavailable")
	}
	var value struct {
		DisableAllHooks bool `json:"disableAllHooks"`
		Hooks           map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type        string `json:"type"`
				Command     string `json:"command"`
				Async       bool   `json:"async"`
				AsyncRewake bool   `json:"asyncRewake"`
				Timeout     int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(settings, &value) != nil || value.DisableAllHooks {
		return out, errors.New("original native hook settings invalid")
	}
	command := legacyShellQuote(python) + " " + legacyShellQuote(module) + " --config " + legacyShellQuote(config)
	qualified := 0
	for _, group := range value.Hooks["PreToolUse"] {
		for _, hook := range group.Hooks {
			if group.Matcher == "*" && hook.Type == "command" && hook.Command == command && !hook.Async && !hook.AsyncRewake && hook.Timeout == 15 {
				qualified++
			}
		}
	}
	if qualified != 1 {
		return out, errors.New("actual synchronous native fence hook unavailable")
	}
	configuration, err := readLegacyFile(config, 65536)
	if err != nil {
		return out, err
	}
	var cfg struct {
		Actor   string `json:"agent_id"`
		Native  string `json:"native_session_id"`
		Runtime string `json:"runtime"`
		Role    string `json:"role"`
		Fence   bool   `json:"legacy_native_fence"`
	}
	if json.Unmarshal(configuration, &cfg) != nil || cfg.Actor != actor || cfg.Native != native || cfg.Runtime != "claude" || cfg.Role != "worker" || !cfg.Fence {
		return out, errors.New("native hook configuration differs from original Worker")
	}
	code, err := readLegacyFile(module, 65536)
	if err != nil || filepath.Base(module) != "claude_native_fence.py" {
		return out, errors.New("selected native hook module unavailable")
	}
	receiver, err := readLegacyFile(filepath.Join(filepath.Dir(module), "terminal_receiver.py"), 65536)
	if err != nil {
		return out, errors.New("selected receiver artifact unavailable")
	}
	out.SettingsSHA256 = legacyBytesHash(settings)
	out.ConfigPath, out.ConfigSHA256 = config, legacyBytesHash(configuration)
	out.ModulePath, out.ModuleSHA256 = module, legacyBytesHash(code)
	out.ReceiverSHA256 = legacyBytesHash(receiver)
	return out, nil
}

func observeLegacyHook(ctx context.Context, actor, native, config string) (nativeHookObservation, error) {
	var out nativeHookObservation
	pythonPID := os.Getppid()
	args, err := legacyProcessArguments(pythonPID)
	if err != nil || len(args) != 4 || args[2] != "--config" || args[3] != config || filepath.Base(args[1]) != "claude_native_fence.py" {
		return out, errors.New("native hook adapter must run directly from the selected Python hook")
	}
	parent, err := legacyPS(ctx, pythonPID, "ppid")
	if err != nil {
		return out, err
	}
	client, err := strconv.Atoi(parent)
	if err != nil {
		return out, err
	}
	// Claude may launch its command hook through one shell. Accept only that
	// exact hook command, never an already-running Bash tool or arbitrary wrapper.
	parentArgs, err := legacyProcessArguments(client)
	if err != nil {
		return out, err
	}
	if len(parentArgs) == 3 && parentArgs[1] == "-c" && parentArgs[2] == legacyShellQuote(args[0])+" "+legacyShellQuote(args[1])+" --config "+legacyShellQuote(config) {
		parent, err = legacyPS(ctx, client, "ppid")
		if err != nil {
			return out, err
		}
		client, err = strconv.Atoi(parent)
		if err != nil {
			return out, err
		}
	}
	identity, err := legacyProcessIdentity(ctx, client)
	if err != nil || !legacyClaudeNative(client, native) {
		return out, errors.New("actual original Claude hook parent unavailable")
	}
	hook, err := legacyHookIdentity(ctx, client, actor, native, args[1], config, args[0])
	if err != nil {
		return out, err
	}
	python, err := legacyProcessIdentity(ctx, pythonPID)
	if err != nil {
		return out, err
	}
	hook.PythonExecutable, hook.PythonSHA256 = python.Executable, python.SHA256
	return nativeHookObservation{Client: identity, Hook: hook}, nil
}

// CheckWorkerNativeHook has no caller-supplied receipt or PID import. It records
// the actual hook execution and eligibility atomically before permitting tools.
func (s *Store) CheckWorkerNativeHook(ctx context.Context, actor, native, config string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	observation, err := observeLegacyHook(ctx, actor, native, config)
	if err != nil {
		return err
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worker_native_fences WHERE repo_id=? AND native_session=?`, s.repoID, native).Scan(&count); err != nil || count != 0 {
			return errors.New("original native is fenced; tool execution blocked")
		}
		rows, err := tx.QueryContext(ctx, `SELECT COALESCE(d.revision,0),COALESCE(d.outcome_id,0),COALESCE(m.body,''),COALESCE(m.agent_id,''),r.reserved_by FROM dispatch_reservations r LEFT JOIN dispatch_decisions d ON d.repo_id=r.repo_id AND d.reservation_key=r.item_id AND d.generation=r.generation LEFT JOIN messages m ON m.repo_id=d.repo_id AND m.id=d.outcome_id WHERE r.repo_id=? AND r.worker_thread_id=? AND (d.worker_agent=? OR (d.worker_agent IS NULL AND EXISTS(SELECT 1 FROM claims c WHERE c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id AND c.agent_id=? AND c.state='held')))`, s.repoID, native, actor, actor)
		if err != nil {
			return err
		}
		var body, author, controller string
		count = 0
		for rows.Next() {
			count++
			if err := rows.Scan(&observation.DecisionRevision, &observation.DecisionOutcomeID, &body, &author, &controller); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil || count != 1 {
			return errors.New("native hook current original Worker binding unavailable")
		}
		var admission LegacyNativeAdmission
		observation.RequestSHA256 = ""
		if author == controller && len(body) <= 16384 && json.Unmarshal([]byte(body), &admission) == nil && admission.Schema == "squad.legacy-worker-native-admission.v1" && validLegacyHash(admission.RequestSHA256) {
			observation.RequestSHA256 = admission.RequestSHA256
		}
		raw, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worker_native_hook_observations(repo_id,native_session,actor,observation,observed_at) VALUES(?,?,?,?,?) ON CONFLICT(repo_id,native_session) DO UPDATE SET actor=excluded.actor,observation=excluded.observation,observed_at=excluded.observed_at`, s.repoID, native, actor, string(raw), s.now().Unix())
		return err
	})
}

func (s *Store) legacyNativeAdmission(ctx context.Context, q WorkerHandoffRequest, digest string, processes []LegacyProcess) (LegacyNativeAdmission, error) {
	var admission LegacyNativeAdmission
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT m.body FROM dispatch_decisions d JOIN messages m ON m.repo_id=d.repo_id AND m.id=d.outcome_id WHERE d.repo_id=? AND d.reservation_key=? AND d.generation=? AND d.revision=? AND d.action='hold' AND d.worker_agent=? AND m.agent_id=? AND m.thread=?`, s.repoID, q.Expected.ItemID, q.Expected.Generation, q.DecisionRevision, q.Claim.Actor, q.Controller.Actor, q.Expected.CanonicalItemID).Scan(&body)
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err != nil || len(body) > 16384 || decoder.Decode(&admission) != nil || decoder.Decode(new(any)) != io.EOF || admission.Schema != "squad.legacy-worker-native-admission.v1" || admission.RequestSHA256 != digest || admission.AuthoritySHA256 != q.HumanAuthoritySHA256 || admission.Client.PID == admission.Lease.PID {
		return admission, errors.New("current controller's exact original-native admission required")
	}
	for _, identity := range []LegacyProcess{admission.Client, admission.Lease} {
		found := false
		for _, process := range processes {
			if process == identity && identity.PID > 1 && identity.Start != "" && filepath.IsAbs(identity.Executable) && validLegacyHash(identity.SHA256) {
				found = true
			}
		}
		if !found {
			return admission, errors.New("actual original client/supervisor differs from controller-selected kernel identity")
		}
	}
	if admission.Lease.PID == 0 || !validLegacyHash(admission.Hook.SettingsSHA256) || !validLegacyHash(admission.Hook.ConfigSHA256) || !validLegacyHash(admission.Hook.ModuleSHA256) || !validLegacyHash(admission.Hook.ReceiverSHA256) || !validLegacyHash(admission.Hook.PythonSHA256) || !filepath.IsAbs(admission.Hook.PythonExecutable) {
		return admission, errors.New("controller-selected installed hook identity unavailable")
	}
	if s.legacyOwnerProbe == nil {
		if admission.Lease.PID == 0 || admission.Client.PID != os.Getppid() {
			return admission, errors.New("stop producer must replace the native Bash shell with exec; no continuing writer ancestor")
		}
		// Re-read the currently selected artifacts; a past hook execution does
		// not qualify changed settings, module or original native process.
		for path, hash := range map[string]string{admission.Hook.SettingsPath: admission.Hook.SettingsSHA256, admission.Hook.ConfigPath: admission.Hook.ConfigSHA256, admission.Hook.ModulePath: admission.Hook.ModuleSHA256, filepath.Join(filepath.Dir(admission.Hook.ModulePath), "terminal_receiver.py"): admission.Hook.ReceiverSHA256} {
			raw, err := readLegacyFile(path, 65536)
			if err != nil || legacyBytesHash(raw) != hash {
				return admission, errors.New("original native hook artifacts changed before stop preparation")
			}
		}
		if err := qualifyLegacyReceivers(ctx, processes, admission); err != nil {
			return admission, err
		}
	}
	return admission, nil
}

func qualifyLegacyReceivers(ctx context.Context, processes []LegacyProcess, admission LegacyNativeAdmission) error {
	ownImage, err := legacyProcessIdentity(ctx, os.Getpid())
	if err != nil {
		return err
	}
	for _, process := range processes {
		if process.PID == admission.Client.PID || process.PID == admission.Lease.PID || process.PID == os.Getpid() {
			continue
		}
		args, err := legacyProcessArguments(process.PID)
		if err != nil {
			return err
		}
		// Only the exact selected read-only receiver, or this same reviewed
		// binary's bounded event listener, may remain beside the exec producer.
		receiver := len(args) == 4 && args[1] == filepath.Join(filepath.Dir(admission.Hook.ModulePath), "terminal_receiver.py") && args[2] == "--config" && args[3] == admission.Hook.ConfigPath && process.Executable == admission.Hook.PythonExecutable && process.SHA256 == admission.Hook.PythonSHA256
		listener := len(args) > 2 && args[1] == "terminal-events" && args[2] == "listen" && process.Executable == ownImage.Executable && process.SHA256 == ownImage.SHA256
		if !receiver && !listener {
			return errors.New("unqualified original child can escape the native fence; join it before preparation")
		}
	}
	return nil
}

func (s *Store) WorkerNativeHookObservation(ctx context.Context, native string) (nativeHookObservation, error) {
	var out nativeHookObservation
	if !controllerToken.MatchString(native) {
		return out, errors.New("exact original native required")
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT observation FROM worker_native_hook_observations WHERE repo_id=? AND native_session=?`, s.repoID, native).Scan(&raw); err != nil {
		return out, err
	}
	if len(raw) > 16384 {
		return out, errors.New("bounded native hook observation required")
	}
	err := json.Unmarshal([]byte(raw), &out)
	return out, err
}

func (s *Store) checkLegacyNativeHook(ctx context.Context, tx *sql.Tx, q WorkerHandoffRequest, admission LegacyNativeAdmission) error {
	var raw string
	var observed, decisionAt, outcomeID int64
	if err := tx.QueryRowContext(ctx, `SELECT o.observation,o.observed_at,m.ts,d.outcome_id FROM worker_native_hook_observations o JOIN dispatch_decisions d ON d.repo_id=o.repo_id AND d.reservation_key=? AND d.generation=? AND d.revision=? JOIN messages m ON m.repo_id=d.repo_id AND m.id=d.outcome_id WHERE o.repo_id=? AND o.native_session=? AND o.actor=?`, q.Expected.ItemID, q.Expected.Generation, q.DecisionRevision, s.repoID, q.Expected.WorkerThreadID, q.Claim.Actor).Scan(&raw, &observed, &decisionAt, &outcomeID); err != nil {
		return errors.New("actual blocking native hook execution has not been observed")
	}
	var value nativeHookObservation
	if json.Unmarshal([]byte(raw), &value) != nil || value.Client != admission.Client || value.Hook != admission.Hook || observed < decisionAt || value.DecisionRevision != q.DecisionRevision || value.DecisionOutcomeID != outcomeID || value.RequestSHA256 != admission.RequestSHA256 {
		return errors.New("blocking native hook observation differs from current admission or original process start")
	}
	return nil
}

func legacyBytesHash(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
