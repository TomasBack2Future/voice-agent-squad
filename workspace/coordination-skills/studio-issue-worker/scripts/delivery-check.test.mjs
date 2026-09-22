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
