import test from 'node:test';
import assert from 'node:assert/strict';
import {checkDelivery,controllableDuration} from './delivery-check.mjs';
const schema='squad.delivery-check.v1', head='a'.repeat(40), base='b'.repeat(40);
const freeze=()=>({diffSha256:'c'.repeat(64),prBodySha256:'d'.repeat(64),workingTreeClean:true,remoteHeadVerified:true,fastChecksPassed:true,knownCorrectionsResolved:true,stabilityEvidence:'local head equals remote PR head after fast gates',companionsRequired:['source','tests','docs'],companionsComplete:['source','tests','docs']});
test('OPEN implementation dependency can pass while acceptance still blocks',()=>{
 const s={schema,action:'dependency',kind:'implementation',issueState:'OPEN',integratedSha:head,ancestorVerified:true,evidence:'merge-and-diff',blockedPhase:'implementation'};
 assert(checkDelivery(s).ok);
 assert(!checkDelivery({...s,kind:'acceptance',accepted:false}).ok);
 assert(!checkDelivery({...s,ancestorVerified:false}).ok);
});
test('live writer and unverified external access cannot be bypassed by closed Issue',()=>{
 for(const kind of ['path-release','external-access'])assert(!checkDelivery({schema,action:'dependency',kind,issueState:'CLOSED',evidence:'issue',blockedPhase:'release'}).ok);
});
test('missing companion and conflicting owner block incomplete assignment',()=>{
 const s={schema,action:'work-package',owner:'dev',issue:'794',claimEvidence:'claim',requirements:[{id:'R1',assertion:'deep page available',test:'browser-page-6',phase:'pre-review'}],paths:[{path:'api',owner:'dev',conflictChecked:true}],requiredCompanions:['api','api.test']};
 assert(!checkDelivery(s).ok);s.paths.push({path:'api.test',owner:'dev',conflictChecked:true});assert(checkDelivery(s).ok);
 s.paths[1].owner='other';assert(!checkDelivery(s).ok);
});
test('100 fast 404s and fixture mismatch are not successful performance',()=>{
 const s={schema,action:'successful-samples',revision:head,fixtureType:'evaluation-run',expectedFixtureType:'evaluation-run',minimumSamples:100,expectedStatuses:[200],samples:Array.from({length:100},()=>({status:404,semanticSuccess:false,durationMs:1}))};
 assert(!checkDelivery(s).ok);s.samples=s.samples.map(()=>({status:200,semanticSuccess:true,durationMs:20}));assert(checkDelivery(s).ok);
 assert(!checkDelivery({...s,fixtureType:'session'}).ok);assert(!checkDelivery({...s,samples:[]}).ok);
});
test('negative latency or declared 404 success cannot fake acceptance',()=>{
 assert(!checkDelivery({schema,action:'successful-samples',revision:head,fixtureType:'run',expectedFixtureType:'run',minimumSamples:1,expectedStatuses:[404],samples:[{status:404,semanticSuccess:true,durationMs:1}]}).ok);
 assert(!checkDelivery({schema,action:'successful-samples',revision:head,fixtureType:'run',expectedFixtureType:'run',minimumSamples:1,expectedStatuses:[200],samples:[{status:200,semanticSuccess:true,durationMs:-1}]}).ok);
});
test('generic cleanup never overrides retained or foreign fixture',()=>{
 const r={id:'volume-1',observedId:'volume-1',evidence:'fresh-inspect',owner:'dev',liveOwner:'dev',liveVerified:true,disposition:'ephemeral',liveDisposition:'ephemeral',concurrentMutator:false,shared:false};
 const s={schema,action:'cleanup',owner:'dev',targets:[r],genericFinalCleanup:true};assert(checkDelivery(s).ok);
 for(const delta of [{retained:true},{disposition:'retain'},{liveDisposition:'retain'},{owner:'other'},{observedId:'volume-2'},{shared:true},{liveVerified:false}])assert(!checkDelivery({...s,targets:[{...r,...delta}]}).ok);
 assert(!checkDelivery({...s,targets:[]}).ok);
});
test('same tuple cannot resample a timeout or completed verdict',()=>{
 const s={schema,action:'review',repo:'repo',pr:1,base,head,completeInput:true,inputEvidence:'full-diff',freeze:freeze(),attempts:[]};assert(checkDelivery(s).ok);
 for(const state of ['running','timeout','approved','blocking','unknown'])assert(!checkDelivery({...s,attempts:[{repo:'repo',pr:1,base,head,state}]}).ok);
 assert(checkDelivery({...s,attempts:[{repo:'repo',pr:1,base,head,state:'pre-sampling-error',modelStarted:false,repairEvidence:'fixed-diff-retrieval',repairVerified:true}]}).ok);
 assert(!checkDelivery({...s,completeInput:false}).ok);
});
test('review admission requires a clean final full diff and complete companions',()=>{
 const s={schema,action:'review',repo:'repo',pr:1,base,head,completeInput:true,inputEvidence:'full-diff',freeze:freeze(),attempts:[]};
 for(const delta of [{workingTreeClean:false},{remoteHeadVerified:false},{fastChecksPassed:false},{knownCorrectionsResolved:false},{stabilityEvidence:''}]) assert(!checkDelivery({...s,freeze:{...freeze(),...delta}}).ok);
 assert(!checkDelivery({...s,inputEvidence:'incremental-diff'}).ok);
 assert(!checkDelivery({...s,freeze:{...freeze(),companionsComplete:['source','tests']}}).ok);
});
test('one PR cannot start another review while any head is in flight',()=>{
 const s={schema,action:'review',repo:'repo',pr:1,base,head,completeInput:true,inputEvidence:'full-diff',freeze:freeze(),attempts:[]};
 assert(!checkDelivery({...s,attempts:[{repo:'repo',pr:1,base:'e'.repeat(40),head:'f'.repeat(40),state:'running'}]}).ok);
 assert(checkDelivery({...s,attempts:[{repo:'repo',pr:1,base:'e'.repeat(40),head:'f'.repeat(40),state:'superseded'}]}).ok);
});
test('unknown schema/action/history fail closed',()=>{
 assert(!checkDelivery({}).ok);assert(!checkDelivery({schema,action:'invented'}).ok);
 assert(!checkDelivery({schema,action:'review',repo:'repo',pr:1,base,head,completeInput:true,inputEvidence:'full',freeze:freeze(),attempts:[]}).ok);
});
test('pause accounting unions overlap and excludes only confirmed causes',()=>{
 assert.deepEqual(controllableDuration(0,100,[{start:20,end:50,confirmed:true,kind:'travel'},{start:40,end:60,confirmed:true,kind:'user-pause'},{start:70,end:90,confirmed:false,kind:'host-offline'},{start:0,end:100,confirmed:true,kind:'unknown'}]),{elapsed:100,excluded:40,controllable:60});
});

const acceptance=()=>({schema,action:'acceptance-readiness',owner:'worker',revision:head,target:'https://api.example.test/project-1',planEvidence:'prepared script hash and read-only capability receipt',access:{target:'https://api.example.test/project-1',executionEvidence:'scoped capability inspected',cleanupEvidence:'delete scope inspected'},humanPrerequisites:[],steps:[{id:'retry',command:'node retry-check.mjs',assertion:'retry reaches completed',evidencePath:'receipts/retry.json',timeoutSeconds:120,mutates:true,disposition:'ephemeral',cleanupCommand:'node cleanup-check.mjs',fixture:{mode:'retained',reference:'run-1@revision-2',verified:true,evidence:'read run-1 state',state:'failed',eligibleStates:['failed','cancelled']}}]});
const failure=()=>({phase:'staging-acceptance',environment:'staging',component:'analysis',componentRevision:head,code:'pipeline_analysis_no_changes',condition:'control fixture returns no changes',kind:'deterministic',attemptId:'run-2:attempt-1:request-1',evidence:'run-2 trace and component logs'});
const sharedFailure=()=>({schema,action:'shared-failure',failure:failure(),previousAttempts:[],decision:'diagnose'});
const blocker=()=>({schema,action:'blocker',phase:'acceptance',operation:'query index metadata',target:'https://api.example.test',basis:'failed-path',attemptedTarget:'https://api.example.test',observed:'scoped role denied metadata read',causeStatus:'verified',evidence:'request-1 sanitized denial',recoveryEvidence:'supported metadata API and role capabilities checked',nextAction:{owner:'access-owner',action:'grant named metadata scope'}});

test('acceptance requires prepared checks, eligible fixtures and execution plus cleanup access',()=>{
 const s=acceptance();assert(checkDelivery(s).ok);
 assert(!checkDelivery({...s,revision:'main'}).ok);
 for(const delta of [{state:'completed'},{verified:false},{evidence:''}])assert(!checkDelivery({...s,steps:[{...s.steps[0],fixture:{...s.steps[0].fixture,...delta}}]}).ok);
 assert(checkDelivery({...s,steps:[{...s.steps[0],fixture:{...s.steps[0].fixture,state:'completed'}}]}).errors.includes('fixture-state'));
 assert(!checkDelivery({...s,access:{...s.access,cleanupEvidence:''}}).ok);
 assert(!checkDelivery({...s,access:{...s.access,target:'http://api.example.test/project-1'}}).ok);
 for(const delta of [{command:''},{assertion:''},{timeoutSeconds:0},{evidencePath:''},{cleanupCommand:''}])assert(!checkDelivery({...s,steps:[{...s.steps[0],...delta}]}).ok);
 assert(!checkDelivery({...s,humanPrerequisites:[{action:'login',owner:'user',completed:false,evidence:'login pending'}]}).ok);
});
test('fixture creation may be prepared before ENV without pretending it already ran',()=>{
 const s=acceptance();s.steps[0].fixture={mode:'create',reference:'negative-fixture-spec@v2',prepared:true,command:'node create-failed-run.mjs',evidence:'supported creation path inspected',state:'failed',eligibleStates:['failed']};
 assert(checkDelivery(s).ok);
 assert(!checkDelivery({...s,steps:[{...s.steps[0],fixture:{...s.steps[0].fixture,prepared:false}}]}).ok);
 s.steps[0].disposition='retain';delete s.steps[0].cleanupCommand;
 assert(!checkDelivery(s).ok);s.steps[0].custodyEvidence='retention and owner agreed';assert(checkDelivery(s).ok);
 s.steps[0].mutates=false;delete s.access.cleanupEvidence;assert(checkDelivery(s).ok);
});
test('unchanged deterministic failure cannot retry and needs an exact claimed repair owner',()=>{
 const s=sharedFailure();assert(checkDelivery(s).ok);
 assert(!checkDelivery({...s,failure:{...failure(),componentRevision:'main'}}).ok);
 assert(checkDelivery({...s,failure:{...failure(),componentRevision:'sha256:'+'e'.repeat(64)}}).ok);
 assert(checkDelivery({...s,decision:'retry'}).errors.includes('unchanged-failure'));
 assert(!checkDelivery({...s,decision:'wait-repair'}).ok);
 const repair={item:'FIX-1',owner:'repair-worker',claimEvidence:'live primary claim receipt',failure:failure()};
 assert(checkDelivery({...s,decision:'wait-repair',repair}).ok);
 assert(!checkDelivery({...s,decision:'wait-repair',repair:{...repair,failure:{...failure(),code:'pipeline_analysis_timed_out'}}}).ok);
 assert(checkDelivery({...s,decision:'retry',changeVerified:true,changedConditionEvidence:'repaired component deployed and verified',retrySafetyEvidence:'prior operation terminal; supported retry is idempotent'}).ok);
 assert(!checkDelivery({...s,decision:'retry',changeVerified:false,changedConditionEvidence:'planned fix'}).ok);
});
test('transient retry is bounded and different failures do not consume its budget',()=>{
 const f={...failure(),code:'upstream_rate_limit',condition:'upstream model 429',kind:'transient'};
 const s={...sharedFailure(),failure:f,decision:'retry',retrySafetyEvidence:'prior operation terminal; supported retry is idempotent'};assert(checkDelivery(s).ok);
 const previous={...f,attemptId:'run-1:attempt-1:request-1'};
 assert(!checkDelivery({...s,retrySafetyEvidence:''}).ok);
 assert(!checkDelivery({...s,retryLimit:0}).ok);
 assert(checkDelivery({...s,previousAttempts:[previous]}).errors.includes('retry-budget-exhausted'));
 assert(checkDelivery({...s,previousAttempts:[f]}).errors.includes('duplicate-failure-attempt'));
 assert(checkDelivery({...s,previousAttempts:[previous,previous]}).errors.includes('duplicate-failure-attempt'));
 for(const delta of [{code:'storage_unavailable'},{condition:'edge connection timeout'},{environment:'production'},{phase:'deployment'},{componentRevision:base},{component:'gateway'}])assert(checkDelivery({...s,previousAttempts:[{...previous,...delta}]}).ok);
 assert(!checkDelivery({...s,retryLimit:2}).ok);
 assert(checkDelivery({...s,previousAttempts:[previous],retryLimit:2,retryPolicyEvidence:'existing runbook allows two retries'}).ok);
 assert(!checkDelivery({...s,previousAttempts:[previous,{...previous,attemptId:'run-0:attempt-1:request-1'}],retryLimit:2,retryPolicyEvidence:'existing runbook allows two retries'}).ok);
 assert(!checkDelivery({...s,failure:{...f,kind:'unknown'}}).ok);
});
test('blocker requires the actual intended path or an explicit authority boundary',()=>{
 const s=blocker();assert(checkDelivery(s).ok);
 assert(checkDelivery({...s,attemptedTarget:'http://api.example.test'}).errors.includes('wrong-failed-path'));
 for(const delta of [{evidence:''},{recoveryEvidence:''},{observed:''},{causeStatus:'suspected'}])assert(!checkDelivery({...s,...delta}).ok);
 assert(!checkDelivery({...s,humanAction:'ask user to contact operator'}).ok);
 assert(checkDelivery({...s,humanAction:'complete login',humanOnlyEvidence:'supported browser requires user MFA'}).ok);
 const boundary={...s,basis:'authority-boundary',authorityEvidence:'assignment excludes production'};delete boundary.attemptedTarget;delete boundary.recoveryEvidence;
 assert(checkDelivery(boundary).ok);assert(!checkDelivery({...boundary,authorityEvidence:''}).ok);
});
test('new evidence actions reject malformed collections without throwing',()=>{
 for(const steps of [null,{},'steps',[null]])assert(!checkDelivery({...acceptance(),steps}).ok);
 for(const humanPrerequisites of [null,{},[null]])assert(!checkDelivery({...acceptance(),humanPrerequisites}).ok);
 for(const previousAttempts of [null,{},[null]])assert(!checkDelivery({...sharedFailure(),previousAttempts}).ok);
 assert(!checkDelivery({...sharedFailure(),failure:null}).ok);
 assert(!checkDelivery({...blocker(),nextAction:null}).ok);
});
