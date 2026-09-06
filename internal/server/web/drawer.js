// drawer.js — rich item detail drawer (inline middle column)

import { fetchJSON, postJSON, escapeHtml, fmtDate, fmtAgo, priClass, copyText } from './util.js';
import { renderMarkdown, splitBodySections, isSafeURL } from './markdown.js';
import { ActivityFeed } from './activity.js';
import { attachScrollAnchor } from './scroll-anchor.js';
import { autolinkDOM } from './autolink.js';
import { attachMentionAutocomplete } from './mention.js';
import { agentsSnapshot, itemsSnapshot, refreshBoard } from './board.js';
import { displayName } from './names.js';
import { renderItemActions, wireItemActions, setOnMutated } from './actions.js';

const drawerEl    = document.getElementById('drawer');
const workspace   = document.querySelector('.workspace');
const drawerBody  = document.getElementById('drawer-body');
const drawerId    = document.getElementById('drawer-id');
const drawerPri   = document.getElementById('drawer-pri');
const closeBtn    = document.getElementById('drawer-close');
const copyMdBtn   = document.getElementById('drawer-copy-md');

let loadedItem = null;  // store the last loaded item JSON for copy

let state = {
  itemId: null,
  feed: null,
  anchor: null,
  headerRefetchTimer: null,
  refreshItem: null,
  hotnessTimer: null,
};

// "live" means an agent recent enough to plausibly still be holding a file.
// Matches the server's active+idle threshold (30 min since last tick).
const LIVE_THRESHOLD_SEC = 30 * 60;

export function openItem(itemID) {
  state.itemId = itemID;
  drawerEl.hidden = false;
  drawerEl.setAttribute('aria-hidden', 'false');
  workspace.dataset.drawer = 'open';
  drawerBody.innerHTML = '<p style="color:var(--fg-mute);padding:20px;">Loading…</p>';
  drawerId.textContent = itemID;
  drawerPri.textContent = '';
  drawerPri.className = 'drawer-pri';
  loadItem(itemID);
}

export function closeDrawer() {
  state.itemId = null;
  state.feed = null;
  state.anchor = null;
  clearInterval(state.hotnessTimer);
  state.hotnessTimer = null;
  drawerEl.hidden = true;
  drawerEl.setAttribute('aria-hidden', 'true');
  workspace.dataset.drawer = 'closed';
}

export function currentItemId() { return state.itemId; }

export function setOnCloseHandler(fn) { state.onClose = fn; }

closeBtn.addEventListener('click', () => {
  const id = state.itemId;
  closeDrawer();
  state.onClose?.(id);
});

// Delegated click handler — drawerBody.innerHTML gets replaced on every
// open/refresh, so per-node listeners would re-bind on every render.
// Bind once on the stable parent and route by data-id.
drawerBody.addEventListener('click', (e) => {
  const chip = e.target.closest('[data-id]');
  if (!chip || !drawerBody.contains(chip)) return;
  if (chip.matches('.dep-chip, .similar-row')) {
    openItem(chip.dataset.id);
  }
});

if (copyMdBtn) {
  copyMdBtn.addEventListener('click', async () => {
    if (!loadedItem) return;
    const md = itemAsMarkdown(loadedItem);
    await copyText(md);
    copyMdBtn.textContent = '✓';
    copyMdBtn.classList.add('copied');
    setTimeout(() => {
      copyMdBtn.textContent = '⎘';
      copyMdBtn.classList.remove('copied');
    }, 1200);
  });
}

function itemAsMarkdown(it) {
  const front = [
    `id: ${it.id}`,
    `title: ${it.title}`,
    `type: ${it.type}`,
    `priority: ${it.priority}`,
    `area: ${it.area}`,
    `status: ${it.status}`,
    `estimate: ${it.estimate}`,
    `risk: ${it.risk}`,
    it.created ? `created: ${it.created}` : '',
    it.updated ? `updated: ${it.updated}` : '',
    (it.references || []).length ? `references:\n${(it.references || []).map(r => '  - ' + r).join('\n')}` : '',
    (it.blocked_by || []).length ? `blocked-by: [${(it.blocked_by || []).join(', ')}]` : '',
    (it.relates_to || []).length ? `relates-to: [${(it.relates_to || []).join(', ')}]` : '',
  ].filter(Boolean).join('\n');
  return `---\n${front}\n---\n\n# ${it.title}\n\n${it.body_markdown || ''}`;
}

async function loadItem(itemID) {
  try {
    const [it, activity] = await Promise.all([
      fetchJSON('/api/items/' + encodeURIComponent(itemID)),
      fetchJSON('/api/items/' + encodeURIComponent(itemID) + '/activity?limit=80'),
    ]);
    loadedItem = it;
    renderDrawer(it, activity);
  } catch (err) {
    drawerBody.innerHTML = `<p style="color:var(--danger);padding:20px;">Error: ${escapeHtml(err.message)}</p>`;
  }
}

function renderDrawer(it, activity) {
  const pri = priClass(it.priority);
  drawerId.textContent = it.id;
  drawerPri.textContent = it.priority || '—';
  drawerPri.className = 'drawer-pri ' + pri;

  const sections = splitBodySections(it.body_markdown || '');

  drawerBody.innerHTML = `
    <h1 class="drawer-title">${escapeHtml(it.title || '')}</h1>
    ${metaRibbon(it)}
    ${claimBanner(it)}
    ${renderItemActions(it)}
    ${progressBar(it.progress_pct || 0)}
    ${depsSection(it)}
    ${referencesSection(it)}
    ${evidenceSection(it)}
    ${acSection(it)}
    ${bodySection(sections)}
    ${stateTimelineSection(it, activity)}
    ${codeSectionPlaceholder(it)}
    ${similarItemsSection(it)}
    <section class="drawer-section">
      <div class="drawer-section-head">Activity <span class="count" id="activity-count"></span></div>
      <div class="activity" id="drawer-activity"></div>
      <button class="unread-pill" id="activity-unread" hidden>new ↓</button>
      <button class="load-older" id="drawer-load-older">Load older</button>
    </section>
    <form class="drawer-compose" id="drawer-compose" data-thread="${escapeHtml(it.id)}">
      <input placeholder="Say something in #${escapeHtml(it.id)}…" autocomplete="off"/>
      <button type="submit">Send</button>
    </form>
  `;

  // autolink ITEM-IDs inside every markdown block + syntax highlight
  drawerBody.querySelectorAll('.md').forEach((md) => {
    autolinkDOM(md);
    md.querySelectorAll('pre code').forEach((block) => {
      if (window.hljs?.highlightElement) {
        try { window.hljs.highlightElement(block); } catch {}
      }
    });
  });

  // dep-chip clicks delegated on drawerBody (see top of file)
  wireItemActions(drawerBody, it);

  // lazy-load evidence list once the section is mounted
  loadEvidence(it.id, drawerBody);

  // lazy-load PR + commit links — done items only
  if (it.status === 'done') loadCodeLinks(it.id, drawerBody);

  // similar-row clicks delegated on drawerBody (see top of file)

  // async: annotate references with hotness (how many LIVE agents currently hold each file)
  annotateReferenceHotness(drawerBody);
  clearInterval(state.hotnessTimer);
  state.hotnessTimer = setInterval(() => annotateReferenceHotness(drawerBody), 30 * 1000);

  // wire references copy
  drawerBody.querySelectorAll('.ref-copy').forEach((b) => {
    b.addEventListener('click', () => {
      copyText(b.dataset.ref);
      b.textContent = 'copied';
      b.classList.add('copied');
      setTimeout(() => {
        b.textContent = 'copy';
        b.classList.remove('copied');
      }, 1200);
    });
  });

  // activity
  const activityHost = drawerBody.querySelector('#drawer-activity');
  const pill = drawerBody.querySelector('#activity-unread');
  state.feed = new ActivityFeed(activityHost);
  state.feed.append(activity);
  const activityCountEl = drawerBody.querySelector('#activity-count');
  if (activityCountEl) activityCountEl.textContent = activity.length ? activity.length + '' : '';

  state.anchor = attachScrollAnchor(drawerBody, pill);
  // On drawer open, show the title first — not the activity feed at the bottom.
  drawerBody.scrollTop = 0;
  requestAnimationFrame(() => state.anchor.forceAtBottomCheck());

  // load older
  drawerBody.querySelector('#drawer-load-older').addEventListener('click', async () => {
    if (!state.feed?.oldestTs) return;
    const more = await fetchJSON(
      '/api/items/' + encodeURIComponent(state.itemId) +
      '/activity?limit=50&before=' + state.feed.oldestTs,
    );
    state.feed.loadOlder(more);
  });

  // mention autocomplete on drawer compose
  const drawerInput = drawerBody.querySelector('#drawer-compose input');
  if (drawerInput) attachMentionAutocomplete(drawerInput, () => agentsSnapshot());

  // compose
  drawerBody.querySelector('#drawer-compose').addEventListener('submit', async (e) => {
    e.preventDefault();
    const input = e.target.querySelector('input');
    const msg = input.value.trim();
    if (!msg) return;
    try {
      await postJSON('/api/messages', { thread: e.target.dataset.thread, body: msg });
      input.value = '';
      input.focus();
    } catch (err) {
      console.warn('send failed', err);
    }
  });
}

function metaRibbon(it) {
  const unresolvedDeps = (it.depends_on || []).length;
  const cells = [
    ['Type',     it.type || '—'],
    ['Area',     it.area || '—'],
    ['Status',   it.status || 'open'],
    ['Estimate', it.estimate || '—'],
    ['Risk',     it.risk || '—',      'risk-' + String(it.risk || '').toLowerCase()],
    ['Created',  fmtDate(it.created)],
    ['Updated',  fmtDate(it.updated)],
  ];
  if (it.epic) cells.push(['Epic', it.epic, 'meta-epic']);
  if (it.parallel) cells.push(['Parallel', '∥']);
  if (unresolvedDeps) cells.push(['Deps', String(unresolvedDeps)]);
  if (it.last_touch) cells.push(['Touched', fmtAgo(it.last_touch) + ' ago']);
  return `
    <div class="meta-ribbon">
      ${cells.map(([label, val, extra]) => `
        <div class="meta-cell">
          <div class="meta-label">${escapeHtml(label)}</div>
          <div class="meta-value ${extra || ''}">${escapeHtml(val)}</div>
        </div>`).join('')}
    </div>`;
}

function claimBanner(it) {
  if (it.current_claim) {
    const c = it.current_claim;
    const ago = c.claimed_at ? fmtAgo(c.claimed_at) + ' ago' : '';
    const pretty = displayName(c.agent_id, c.display_name);
    const recovery = c.state === 'recovering'
      ? ` · recovery generation ${escapeHtml(c.generation || '?')}${c.previous_agent_id ? ' from ' + escapeHtml(c.previous_agent_id) : ''}`
      : (c.generation > 1 ? ` · generation ${escapeHtml(c.generation)}` : '');
    const worktreeHtml = c.worktree
      ? `<div class="claim-worktree" title="${escapeHtml(c.worktree)}">worktree: <code>${escapeHtml(c.worktree)}</code></div>`
      : '';
    return `
      <div class="claim-banner">
        <span class="agent-id" title="${escapeHtml(c.agent_id)}">${escapeHtml(pretty)}</span>
        <span class="intent">— ${escapeHtml(c.intent || '(no intent)')}${ago ? ' · claimed ' + ago : ''}${recovery}</span>
        ${worktreeHtml}
      </div>`;
  }
  return `<div class="claim-banner unclaimed"><span class="agent-id">unclaimed</span></div>`;
}

function progressBar(pct) {
  return `<div class="progress"><div class="progress-bar" style="width:${pct}%"></div></div>`;
}

function depsSection(it) {
  const blocking = (it.blocked_by || []);
  const relates  = (it.relates_to || []);
  if (!blocking.length && !relates.length) return '';
  let html = `<section class="drawer-section"><div class="drawer-section-head">Dependencies</div>`;
  if (blocking.length) {
    html += `<div class="deps-block">` +
      blocking.map((id) => `<span class="dep-chip blocking" data-id="${escapeHtml(id)}">blocked by ${escapeHtml(id)}</span>`).join('') +
      `</div>`;
  }
  if (relates.length) {
    html += `<div class="deps-block">` +
      relates.map((id) => `<span class="dep-chip" data-id="${escapeHtml(id)}">relates to ${escapeHtml(id)}</span>`).join('') +
      `</div>`;
  }
  return html + `</section>`;
}

function referencesSection(it) {
  const refs = (it.references || []);
  if (!refs.length) return '';
  return `
    <section class="drawer-section">
      <div class="drawer-section-head">References <span class="count">${refs.length}</span></div>
      <ul class="refs-list">
        ${refs.map((r) => `
          <li class="ref-row" data-ref-path="${escapeHtml(refFilePath(r))}">
            <code>${escapeHtml(r)}</code>
            <span class="ref-hotness" data-hot></span>
            <button type="button" class="ref-copy" data-ref="${escapeHtml(r)}">copy</button>
          </li>`).join('')}
      </ul>
    </section>`;
}

function refFilePath(ref) {
  // ref may be "foo/bar.go:72-96" — strip the line range for matching touches
  return String(ref).split(':')[0];
}

async function annotateReferenceHotness(root) {
  const rows = [...root.querySelectorAll('.ref-row[data-ref-path]')];
  if (!rows.length) return;

  let touches = [], agents = [];
  try {
    [touches, agents] = await Promise.all([
      fetchJSON('/api/touches?active=true'),  // only un-released touches
      fetchJSON('/api/agents'),
    ]);
  } catch { return; }

  const now = Math.floor(Date.now() / 1000);
  const live = new Set();
  for (const a of (agents || [])) {
    const id = a.agent_id || a.AgentID;
    const lastTick = a.last_tick || a.LastTick || 0;
    if (!id) continue;
    if (lastTick && now - lastTick <= LIVE_THRESHOLD_SEC) live.add(id);
  }

  const byPath = new Map();
  for (const t of (touches || [])) {
    const p = t.path || t.Path;
    const agent = t.agent_id || t.AgentID;
    if (!p || !agent) continue;
    if (!live.has(agent)) continue;                  // skip stale/offline agents
    if (!byPath.has(p)) byPath.set(p, new Set());
    byPath.get(p).add(agent);
  }

  for (const row of rows) {
    const path = row.dataset.refPath;
    const hot = row.querySelector('[data-hot]');
    if (!hot) continue;
    const heldBy = byPath.get(path);
    if (!heldBy || !heldBy.size) {
      hot.classList.remove('hot');
      hot.textContent = '';
      hot.removeAttribute('title');
      continue;
    }
    const n = heldBy.size;
    hot.classList.add('hot');
    hot.textContent = `${n} holding`;
    hot.title = [...heldBy].join(', ');
  }
}

// re-run hotness on demand (e.g. from SSE touch/untouch events)
export function refreshHotness() {
  if (!state.itemId) return;
  annotateReferenceHotness(drawerBody);
}

function evidenceSection(it) {
  const required = (it.evidence_required || []);
  // section is rendered as a placeholder; populated lazily by loadEvidence().
  return `
    <section class="drawer-section evidence-section" data-evidence-host>
      <div class="drawer-section-head">Evidence <span class="count" data-ev-count></span></div>
      ${required.length ? `
        <div class="ev-required" data-ev-required>
          <div class="ev-required-head">Required</div>
          <ul class="ev-required-list">
            ${required.map((k) => `<li data-ev-kind="${escapeHtml(k)}"><span class="ev-status" data-ev-status>—</span> ${escapeHtml(k)}</li>`).join('')}
          </ul>
        </div>` : ''}
      <ul class="ev-list" data-ev-list><li class="ev-loading">loading…</li></ul>
    </section>`;
}

async function loadEvidence(itemId, root) {
  const list = root.querySelector('[data-ev-list]');
  const countEl = root.querySelector('[data-ev-count]');
  if (!list) return;
  try {
    const recs = await fetchJSON('/api/items/' + encodeURIComponent(itemId) + '/attestations');
    countEl.textContent = recs.length ? recs.length + '' : '';
    if (!recs.length) {
      list.innerHTML = '<li class="ev-empty">no attestations yet</li>';
    } else {
      list.innerHTML = recs.map((r) => `
        <li class="ev-row">
          <span class="ev-kind">${escapeHtml(r.kind)}</span>
          <code class="ev-cmd" title="${escapeHtml(r.command)}">${escapeHtml(r.command)}</code>
          <span class="ev-exit ${r.exit_code === 0 ? 'ok' : 'bad'}">exit ${r.exit_code}</span>
          <span class="ev-hash" title="${escapeHtml(r.output_hash)}">${escapeHtml(String(r.output_hash || '').slice(0, 8))}</span>
          <span class="ev-when">${escapeHtml(fmtAgo(r.created_at))} ago</span>
          <span class="ev-agent">${escapeHtml(r.agent_id)}</span>
        </li>`).join('');
    }
    // tick the required-status checks
    const haveKinds = new Set(recs.map((r) => r.kind));
    root.querySelectorAll('[data-ev-kind]').forEach((li) => {
      const k = li.dataset.evKind;
      const status = li.querySelector('[data-ev-status]');
      if (haveKinds.has(k)) { status.textContent = '✓'; status.className = 'ev-status ok'; }
      else                  { status.textContent = '✗'; status.className = 'ev-status bad'; }
    });
  } catch (err) {
    list.innerHTML = `<li class="ev-empty">${escapeHtml(err.message)}</li>`;
  }
}

function acSection(it) {
  const ac = it.ac || [];
  if (!ac.length) return '';
  const done = ac.filter((x) => x.checked).length;
  return `
    <section class="drawer-section">
      <div class="drawer-section-head">Acceptance <span class="count">${done}/${ac.length}</span></div>
      <ul class="ac-list">
        ${ac.map((a) => `
          <li class="ac-item${a.checked ? ' checked' : ''}">
            <span class="ac-glyph">${a.checked ? '▣' : '□'}</span>
            <span class="ac-text">${escapeHtml(a.text).replace(/\n/g, '<br>')}</span>
          </li>`).join('')}
      </ul>
    </section>`;
}

function bodySection(sections) {
  if (!sections.length) return '';
  return `
    <section class="drawer-section">
      <div class="drawer-section-head">Detail</div>
      ${sections.map((s, i) => `
        <details class="body-section"${i === 0 ? ' open' : ''}>
          <summary>${escapeHtml(s.title)}</summary>
          <div class="md">${renderMarkdown(s.content)}</div>
        </details>`).join('')}
    </section>`;
}

// ---- state timeline -----------------------------------------------------
function stateTimelineSection(it, activity) {
  // events that matter for state: claim, release, done, blocked, reassign
  const pts = [];
  if (it.created) pts.push({ label: 'created', ts: dateToTs(it.created), kind: 'created' });
  for (const e of [...activity].sort((a, b) => a.ts - b.ts)) {
    if (['claim','release','done','blocked','reassign'].includes(e.kind)) {
      const pretty = e.agent_id ? displayName(e.agent_id, e.display_name) : '';
      pts.push({
        label: `${e.kind}${pretty ? ' · ' + pretty : ''}${e.outcome ? ' (' + e.outcome + ')' : ''}`,
        ts: e.ts,
        kind: e.kind,
      });
    }
  }
  if (pts.length < 2) return '';

  const now = Math.floor(Date.now() / 1000);
  const t0 = pts[0].ts;
  const tN = Math.max(now, pts[pts.length - 1].ts);
  const span = Math.max(1, tN - t0);

  const dots = pts.map((p) => {
    const pct = ((p.ts - t0) / span) * 100;
    return `<span class="tl-dot tl-${p.kind}" style="left:${pct.toFixed(2)}%"
      title="${escapeHtml(p.label)} · ${escapeHtml(fmtRelShort(p.ts, now))}"></span>`;
  }).join('');

  const labels = pts.map((p) => `
    <div class="tl-label">
      <span class="tl-kind tl-${p.kind}">${escapeHtml(p.kind)}</span>
      <span class="tl-when">${escapeHtml(fmtRelShort(p.ts, now))}</span>
      ${p.label.includes('·') ? `<span class="tl-detail">${escapeHtml(p.label.split('·').slice(1).join('·').trim())}</span>` : ''}
    </div>`).join('');

  return `
    <section class="drawer-section">
      <div class="drawer-section-head">State timeline</div>
      <div class="timeline">
        <div class="tl-track"><div class="tl-line"></div>${dots}</div>
        <div class="tl-labels">${labels}</div>
      </div>
    </section>`;
}

function dateToTs(d) {
  // "YYYY-MM-DD" or ISO — use noon UTC to avoid timezone drift
  if (!d) return 0;
  const dt = new Date(d + 'T12:00:00Z');
  return Math.floor(dt.getTime() / 1000);
}

// ---- code links (PR + commits, done items only) -------------------------
function codeSectionPlaceholder(it) {
  if (it.status !== 'done') return '';
  return `
    <section class="drawer-section" id="drawer-code" data-state="loading">
      <div class="drawer-section-head">Code</div>
      <div class="code-skel"></div>
    </section>`;
}

async function loadCodeLinks(itemID, root) {
  const host = root.querySelector('#drawer-code');
  if (!host) return;
  let data;
  try {
    data = await fetchJSON(`/api/items/${encodeURIComponent(itemID)}/links`);
  } catch (err) {
    // Silently drop the section on failure — the user has no action they
    // can take. The dashboard's network indicator already covers connectivity.
    if (currentItemId() === itemID) host.remove();
    return;
  }
  // Drawer may have moved on to another item while we were waiting — bail
  // rather than stamp this item's data into someone else's drawer body.
  if (currentItemId() !== itemID) return;

  const pr = data?.pr || null;
  const commits = data?.commits || [];
  if (!pr && !commits.length) {
    host.remove();
    return;
  }

  const parts = [];
  if (pr) {
    const labelText = pr.number ? `PR #${pr.number} ${pr.branch || ''}` : `Branch ${pr.branch || ''}`;
    if (pr.url && isSafeURL(pr.url)) {
      parts.push(`<a class="code-row code-pr" href="${escapeHtml(pr.url)}" target="_blank" rel="noopener">${escapeHtml(labelText)}</a>`);
    } else {
      parts.push(`<div class="code-row code-pr">${escapeHtml(labelText)}</div>`);
    }
  }
  for (const c of commits) {
    const short = (c.sha || '').slice(0, 7);
    const subject = c.subject || '';
    if (c.url && isSafeURL(c.url)) {
      parts.push(`
        <a class="code-row code-commit" href="${escapeHtml(c.url)}" target="_blank" rel="noopener" title="${escapeHtml(subject)}">
          <code class="code-sha">${escapeHtml(short)}</code>
          <span class="code-subject">${escapeHtml(subject)}</span>
        </a>`);
    } else {
      parts.push(`
        <div class="code-row code-commit" title="${escapeHtml(subject)}">
          <code class="code-sha">${escapeHtml(short)}</code>
          <span class="code-subject">${escapeHtml(subject)}</span>
        </div>`);
    }
  }
  host.dataset.state = 'loaded';
  host.innerHTML = `
    <div class="drawer-section-head">Code</div>
    <div class="code-list">${parts.join('')}</div>`;
}

function fmtRelShort(ts, now) {
  const s = Math.abs(now - ts);
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.round(s / 60) + 'm ago';
  if (s < 86400) return Math.round(s / 3600) + 'h ago';
  return Math.round(s / 86400) + 'd ago';
}

// ---- similar items ------------------------------------------------------
function similarItemsSection(it) {
  const all = itemsSnapshot();
  const myTokens = tokensFor(it.title);
  const scored = [];
  for (const other of all) {
    if (other.id === it.id || other.status === 'done') continue;
    let score = 0;
    if (other.area && other.area === it.area) score += 3;
    if (other.type && other.type === it.type) score += 1;
    const otherTokens = tokensFor(other.title);
    for (const t of myTokens) if (otherTokens.has(t)) score += 2;
    if (score >= 4) scored.push({ item: other, score });
  }
  scored.sort((a, b) => b.score - a.score);
  const top = scored.slice(0, 5);
  if (!top.length) return '';
  return `
    <section class="drawer-section">
      <div class="drawer-section-head">Similar items <span class="count">${top.length}</span></div>
      <ul class="similar-list">
        ${top.map((s) => `
          <li class="similar-row" data-id="${escapeHtml(s.item.id)}">
            <span class="similar-id">${escapeHtml(s.item.id)}</span>
            <span class="similar-pri pri-pip ${priClass(s.item.priority)}">${escapeHtml((s.item.priority || '').replace(/^P/, ''))}</span>
            <span class="similar-title">${escapeHtml(s.item.title)}</span>
            <span class="similar-area">${escapeHtml(s.item.area || '')}</span>
          </li>`).join('')}
      </ul>
    </section>`;
}

const STOP = new Set(['the','a','an','of','and','or','to','for','in','on','with','by','at','is','are','per','from','vs','into','when','using','use','uses','as','not','no']);
function tokensFor(title) {
  const out = new Set();
  for (const raw of String(title || '').toLowerCase().split(/[^a-z0-9]+/)) {
    if (raw && raw.length > 2 && !STOP.has(raw)) out.add(raw);
  }
  return out;
}

// ---- SSE dispatch ------------------------------------------------------

export function onEvent(payload, kind) {
  if (!state.itemId) return false;
  const target = payload.item_id || payload.thread;
  if (target !== state.itemId) return false;

  // append to activity
  if (state.feed) {
    const e = {
      kind,
      ts: payload.ts || Math.floor(Date.now() / 1000),
      agent_id: payload.agent_id || '',
      detail: payload.intent || payload.note || '',
      outcome: payload.outcome || '',
      pct: payload.pct,
      path: payload.path || '',
      body: payload.body || '',
    };
    state.feed.prepend(e);
    state.anchor?.onContentChange();
  }

  // header refetch for claim/release/progress/done/blocked/reassign
  if (['claim','release','progress','done','blocked','reassign'].includes(kind)) {
    queueHeaderRefresh();
  }
  return true;
}

function queueHeaderRefresh() {
  clearTimeout(state.headerRefetchTimer);
  state.headerRefetchTimer = setTimeout(async () => {
    if (!state.itemId) return;
    try {
      const it = await fetchJSON('/api/items/' + encodeURIComponent(state.itemId));
      const body = document.getElementById('drawer-body');
      if (!body) return;
      const banner = body.querySelector('.claim-banner');
      const bar = body.querySelector('.progress-bar');
      if (banner) banner.outerHTML = claimBanner(it);
      if (bar) bar.style.width = (it.progress_pct || 0) + '%';
      drawerPri.textContent = it.priority || '—';
      drawerPri.className = 'drawer-pri ' + priClass(it.priority);
    } catch {}
  }, 250);
}
