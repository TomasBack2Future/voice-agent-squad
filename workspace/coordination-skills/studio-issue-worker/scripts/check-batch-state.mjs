// Read-only audit of an explicitly supplied, independently verified snapshot.
// This is not an ownership mechanism: only Squad atomic claim authorizes work.
export function auditBatch(s) {
 const errors=[];const tasks=s.tasks.filter(t=>!t.terminal||t.externalOperation||t.claims.length);
 const unbound=s.reservations.filter(r=>r.active&&!r.task);
 if(new Set(tasks.map(t=>t.id)).size+unbound.length>5)errors.push('global-wip');
 const batchTasks=tasks.filter(t=>t.batch===s.batch);
 if(batchTasks.filter(t=>t.role==='developer').length>2)errors.push('development-limit');
 if(batchTasks.filter(t=>t.role==='integration-release').length>1)errors.push('integration-limit');
 const owner=batchTasks.find(t=>t.role==='integration-release');
 if(owner&&(!owner.claims.includes(s.primary)||owner.claims.length>2))errors.push('primary-claim');
 for(const task of tasks){if(task.claims.length>2)errors.push('claim-limit');if(task.role==='developer'&&task.claims.some(c=>c.startsWith('ENV-')))errors.push('developer-env');}
 const canonical=new Set();
 for(const r of s.reservations.filter(r=>r.active)){if(canonical.has(r.source))errors.push('duplicate-reservation');canonical.add(r.source);}
 for(const c of s.children){
  const r=s.reservations.find(r=>r.source===c.source&&r.generation===c.generation&&r.active);
  if(c.state!=='closed'&&!r)errors.push(`${c.id}:missing-reservation`);
  if(['handoff-committed','integrated','exact-SHA-accepted'].includes(c.state)){
   if(!c.hold||c.claimant)errors.push(`${c.id}:handoff-hold`);
   const e=c.events;
   if(!e.offer||!e.ack||!e.release||!e.receipt)errors.push(`${c.id}:incomplete-handoff`);
   else if(!(e.offer.seq<e.ack.seq&&e.ack.seq<e.release.seq&&e.release.seq<e.receipt.seq)||new Set(Object.values(e).map(x=>x.head)).size!==1||e.ack.owner!==s.owner||e.receipt.owner!==s.owner)errors.push(`${c.id}:handoff-tuple`);
  }
  if(['exact-SHA-accepted','closed'].includes(c.state)&&(!c.acceptance||c.acceptance.sha!==s.acceptedSha||!c.acceptance.functional||!c.acceptance.cleanup||!c.acceptance.healthyUnmixed))errors.push(`${c.id}:acceptance`);
  if(['exact-SHA-accepted','closed'].includes(c.state)){
   const a=c.acceptance;const l=a?.logs;
   const text=v=>typeof v==='string'&&v.trim().length>0;
   const verified=l?.status==='verified'&&text(s.acceptedSha)&&l.sha===s.acceptedSha&&text(l.evidence);
   const notApplicable=l?.status==='not-applicable'&&l.affectedStructuredLogPath===false&&text(l.reason);
   if(!verified&&!notApplicable)errors.push(`${c.id}:log-evidence`);
  }
  if(c.proposedWriter&&c.claimant&&c.proposedWriter!==c.claimant)errors.push(`${c.id}:duplicate-writer`);
  if(c.proposedWriter===s.owner&&c.state==='implementation-ready')errors.push(`${c.id}:premature-integration-write`);
 }
 const heldDispatch = s.children.some(c => c.proposedDispatch && (c.hold || c.claimant || s.reservations.some(r => r.active && r.source === c.source)));
 if (s.proposedAction === 'dispatch' && (!s.ready || !s.ciDemonstrated || !s.policyCurrent || heldDispatch)) errors.push('dispatch-hold');
 if(s.proposedAction==='release'&&(!owner||!owner.claims.includes('ENV-001')||!s.policyCurrent||!s.ciGreen||!s.reviewAuthorized||!s.sourceTerminal||!s.prefetchGrouped||s.runnerDeadlock))errors.push('release-gate');
 if(s.proposedAction==='a14'&&(!s.implementationsReady||!s.priorEvidenceReady))errors.push('a14-readiness');
 return {schema:'studio.batch_audit.v1',advisory:true,errors};
}
