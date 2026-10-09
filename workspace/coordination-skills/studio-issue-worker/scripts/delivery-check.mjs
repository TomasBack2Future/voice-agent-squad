// Read-only evidence preflight. It neither claims resources nor executes mutations.
import {readFileSync,realpathSync} from 'node:fs';
import {fileURLToPath} from 'node:url';

const text = x => typeof x === 'string' && x.trim().length > 0;
const sha = x => typeof x === 'string' && /^[0-9a-f]{40}$/.test(x);
const record = x => x !== null && typeof x === 'object' && !Array.isArray(x);
const immutable = x => sha(x) || (typeof x === 'string' && /^sha256:[0-9a-f]{64}$/.test(x));
const failureFields = ['phase','environment','component','componentRevision','code','condition'];
const validFailure = f => record(f) && failureFields.every(k => text(f[k])) && immutable(f.componentRevision) && text(f.attemptId) && text(f.evidence) && ['deterministic','transient','unknown'].includes(f.kind);
const failureKey = f => JSON.stringify(failureFields.map(k => f[k]));
export function checkDelivery(s) {
  const errors = [];
  if (!s || s.schema !== 'squad.delivery-check.v1') return {ok:false, errors:['schema']};
  switch (s.action) {
    case 'work-package': {
      if (!text(s.owner) || !text(s.issue) || !text(s.claimEvidence)) errors.push('ownership');
      if (!Array.isArray(s.requirements) || !s.requirements.length) errors.push('requirements');
      for (const r of s.requirements || []) {
        if (!text(r.id) || !text(r.assertion) || !text(r.test) || !text(r.phase)) errors.push('untestable-requirement');
      }
      if (!Array.isArray(s.paths) || !s.paths.length) errors.push('paths');
      for (const p of s.paths || []) {
        if (!text(p.path) || p.owner !== s.owner || p.conflictChecked !== true) errors.push('path-owner');
      }
      if (!Array.isArray(s.requiredCompanions)) errors.push('companion-audit');
      for (const p of s.requiredCompanions || []) {
        if (!(s.paths || []).some(x => x.path === p)) errors.push(`missing-companion:${p}`);
      }
      break;
    }
    case 'dependency': {
      if (!['implementation','path-release','acceptance','external-access'].includes(s.kind)) errors.push('dependency-kind');
      if (!text(s.evidence) || !text(s.blockedPhase)) errors.push('dependency-evidence');
      if (s.kind === 'implementation' && (!sha(s.integratedSha) || s.ancestorVerified !== true)) errors.push('implementation-missing');
      if (s.kind === 'path-release' && s.writerReleased !== true) errors.push('writer-live');
      if (s.kind === 'acceptance' && s.accepted !== true) errors.push('acceptance-pending');
      if (s.kind === 'external-access' && (s.targetVerified !== true || s.accessVerified !== true)) errors.push('external-unverified');
      break;
    }
    case 'acceptance-readiness': {
      if (!text(s.owner) || !text(s.target) || !text(s.planEvidence)) errors.push('acceptance-plan');
      if (!sha(s.revision)) errors.push('acceptance-revision');
      if (!record(s.access) || s.access.target !== s.target || !text(s.access.executionEvidence)) errors.push('acceptance-access');
      if (!Array.isArray(s.steps) || !s.steps.length) errors.push('acceptance-steps');
      for (const r of Array.isArray(s.steps) ? s.steps : []) {
        if (!record(r)) { errors.push('acceptance-step'); continue; }
        if (!['id','command','assertion','evidencePath'].every(k => text(r[k])) || !Number.isInteger(r.timeoutSeconds) || r.timeoutSeconds < 1 || typeof r.mutates !== 'boolean') errors.push('acceptance-step');
        const f = r.fixture;
        if (!record(f) || !text(f.reference) || !text(f.evidence)) errors.push('fixture-evidence');
        if (!record(f) || !text(f.state) || !Array.isArray(f.eligibleStates) || !f.eligibleStates.length || f.eligibleStates.some(x => !text(x)) || !f.eligibleStates.includes(f.state)) errors.push('fixture-state');
        if (!record(f) || (f.mode === 'retained' ? f.verified !== true : f.mode === 'create' ? f.prepared !== true || !text(f.command) : true)) errors.push('fixture-preparation');
        if (r.mutates === true) {
          if (r.disposition === 'ephemeral') {
            if (!text(r.cleanupCommand) || !record(s.access) || !text(s.access.cleanupEvidence)) errors.push('acceptance-cleanup');
          } else if (r.disposition !== 'retain' || !text(r.custodyEvidence)) errors.push('acceptance-custody');
        }
      }
      if (!Array.isArray(s.humanPrerequisites)) errors.push('human-prerequisites-unknown');
      for (const h of Array.isArray(s.humanPrerequisites) ? s.humanPrerequisites : []) {
        if (!record(h) || !text(h.action) || !text(h.owner) || h.completed !== true || !text(h.evidence)) errors.push('human-prerequisite-pending');
      }
      break;
    }
    case 'shared-failure': {
      const f = s.failure;
      if (!validFailure(f)) { errors.push('failure-evidence'); break; }
      if (!Array.isArray(s.previousAttempts) || s.previousAttempts.some(x => !validFailure(x))) { errors.push('failure-history'); break; }
      const ids = s.previousAttempts.map(x => x.attemptId);
      if (ids.includes(f.attemptId) || new Set(ids).size !== ids.length) errors.push('duplicate-failure-attempt');
      if (!['diagnose','wait-repair','retry'].includes(s.decision)) errors.push('failure-decision');
      if (s.decision === 'wait-repair') {
        const r = s.repair;
        if (!record(r) || !text(r.item) || !text(r.owner) || !text(r.claimEvidence) || !validFailure(r.failure) || failureKey(r.failure) !== failureKey(f)) errors.push('repair-owner');
      }
      if (s.decision === 'retry') {
        if (!text(s.retrySafetyEvidence)) errors.push('retry-safety');
        // An actual repaired/changed condition permits targeted revalidation.
        // A planned fix, different Worker, or unrelated merge does not.
        if (s.changeVerified === true && text(s.changedConditionEvidence)) break;
        if (f.kind !== 'transient') { errors.push('unchanged-failure'); break; }
        const limit = s.retryLimit === undefined ? 1 : s.retryLimit;
        if (!Number.isInteger(limit) || limit < 0 || (limit > 1 && !text(s.retryPolicyEvidence))) errors.push('retry-policy');
        else if (s.previousAttempts.filter(x => failureKey(x) === failureKey(f)).length >= limit) errors.push('retry-budget-exhausted');
      }
      break;
    }
    case 'blocker': {
      if (!['phase','operation','target','observed','evidence'].every(k => text(s[k])) || !['verified','unknown'].includes(s.causeStatus)) errors.push('blocker-evidence');
      if (s.basis === 'failed-path') {
        if (!text(s.attemptedTarget) || s.attemptedTarget !== s.target) errors.push('wrong-failed-path');
        if (!text(s.recoveryEvidence)) errors.push('blocker-recovery');
      } else if (s.basis !== 'authority-boundary' || !text(s.authorityEvidence)) errors.push('blocker-basis');
      if (!record(s.nextAction) || !text(s.nextAction.owner) || !text(s.nextAction.action)) errors.push('blocker-next-action');
      if (s.humanAction !== undefined && (!text(s.humanAction) || !text(s.humanOnlyEvidence))) errors.push('human-only-unverified');
      break;
    }
    case 'successful-samples': {
      if (!sha(s.revision) || !text(s.fixtureType) || s.fixtureType !== s.expectedFixtureType) errors.push('sample-identity');
      if (!Number.isInteger(s.minimumSamples) || s.minimumSamples < 1 || !Array.isArray(s.samples) || s.samples.length < s.minimumSamples) errors.push('sample-count');
      if (!Array.isArray(s.expectedStatuses) || !s.expectedStatuses.length || s.expectedStatuses.some(x => !Number.isInteger(x) || x < 200 || x > 299)) errors.push('expected-success-status');
      for (const r of s.samples || []) {
        if (!(s.expectedStatuses || []).includes(r.status) || r.status < 200 || r.status > 299 || r.semanticSuccess !== true || !Number.isFinite(r.durationMs) || r.durationMs < 0) errors.push('invalid-success-sample');
      }
      break;
    }
    case 'cleanup': {
      if (!text(s.owner) || !Array.isArray(s.targets) || !s.targets.length) errors.push('cleanup-targets');
      for (const r of s.targets || []) {
        if (!text(r.id) || r.id !== r.observedId || !text(r.evidence) || r.liveVerified !== true) errors.push('cleanup-identity');
        if (r.owner !== s.owner || r.liveOwner !== s.owner) errors.push('cleanup-custodian');
        if (r.disposition !== 'ephemeral' || r.liveDisposition !== 'ephemeral' || r.retained === true) errors.push('cleanup-retention');
        if (r.concurrentMutator !== false || r.shared !== false) errors.push('cleanup-shared');
      }
      break;
    }
    case 'review': {
      if (!text(s.repo) || !Number.isInteger(s.pr) || s.pr < 1 || !sha(s.base) || !sha(s.head)) errors.push('review-tuple');
      if (s.completeInput !== true || s.inputEvidence !== 'full-diff') errors.push('review-input');
      const f=s.freeze;
      if (!f || !/^[0-9a-f]{64}$/.test(f.diffSha256||'') || !/^[0-9a-f]{64}$/.test(f.prBodySha256||'')) errors.push('review-freeze-hash');
      if (!f || f.workingTreeClean!==true || f.remoteHeadVerified!==true || f.fastChecksPassed!==true || f.knownCorrectionsResolved!==true || !text(f.stabilityEvidence)) errors.push('review-admission');
      if (!f || !Array.isArray(f.companionsRequired) || !f.companionsRequired.length || !Array.isArray(f.companionsComplete)) errors.push('review-companions');
      else for (const c of f.companionsRequired) if (!text(c) || !f.companionsComplete.includes(c)) errors.push(`missing-companion:${c}`);
      if (!Array.isArray(s.attempts)) errors.push('review-history-unknown');
      for (const a of s.attempts || []) {
        if (a.repo !== s.repo || a.pr !== s.pr) continue;
        if (a.state === 'running') { errors.push('review-inflight'); continue; }
        if (a.base !== s.base || a.head !== s.head) continue;
        if (a.state === 'pre-sampling-error' && a.modelStarted === false && text(a.repairEvidence) && a.repairVerified === true) continue;
        errors.push('review-existing-attempt');
      }
      break;
    }
    default: errors.push('unknown-action');
  }
  return {schema:s.schema, action:s.action, advisory:true, ok:errors.length===0, errors:[...new Set(errors)]};
}

// Do not double-count overlapping pauses, or silently count unverified silence as downtime.
export function controllableDuration(start, end, exclusions) {
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) throw new Error('invalid interval');
  const spans = exclusions.filter(x => x.confirmed === true && ['user-pause','travel','host-offline'].includes(x.kind))
    .map(x => [Math.max(start,x.start),Math.min(end,x.end)])
    .filter(([a,b]) => Number.isFinite(a) && Number.isFinite(b) && b>a).sort((a,b)=>a[0]-b[0]);
  let excluded=0, left=null, right=null;
  for (const [a,b] of spans) {
    if (left===null) {left=a;right=b;} else if(a<=right) right=Math.max(right,b);
    else {excluded+=right-left;left=a;right=b;}
  }
  if(left!==null) excluded+=right-left;
  return {elapsed:end-start,excluded,controllable:end-start-excluded};
}

function isMainEntry() {
  if (!process.argv[1]) return false;
  try { return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url)); } catch { return false; }
}

if (isMainEntry()) {
  try {
    const result=checkDelivery(JSON.parse(readFileSync(process.argv[2], 'utf8')));
    console.log(JSON.stringify(result)); process.exitCode=result.ok?0:2;
  } catch { console.error('Invalid or unreadable delivery snapshot'); process.exitCode=2; }
}
