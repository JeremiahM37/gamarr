// Gamarr frontend. All behavior lives here (no inline scripts / handlers) so
// the app runs under a strict Content-Security-Policy (script-src 'self').

const HOST = location.hostname;
let searchResults = [], allPlatforms = [], currentTab = 'search', dlPollTimer = null, libPage = 1;

document.addEventListener('DOMContentLoaded', () => { loadPlatforms(); loadConfig(); startBgPoll(); });

function switchTab(tab) {
  closeMobileNav();
  currentTab = tab;
  document.querySelectorAll('.nav-tab').forEach(b => { b.classList.remove('active'); b.style.borderColor = 'transparent'; });
  document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
  const btn = document.querySelector(`[data-tab="${tab}"]`);
  if (btn) { btn.classList.add('active'); btn.style.borderColor = '#6366f1'; }
  document.getElementById('tab-' + tab).classList.add('active');
  if (tab === 'downloads') { pollDownloads(); dlPollTimer = setInterval(pollDownloads, 5000); }
  else if (dlPollTimer) { clearInterval(dlPollTimer); dlPollTimer = null; }
  if (tab === 'library') loadLibrary();
  if (tab === 'wishlist') loadWishlist();
  if (tab === 'settings') { loadSettings(); loadSources(); loadStats(); loadActivity(); loadMonitor(); }
}

function startBgPoll() { setInterval(updateBadge, 15000); }

async function loadConfig() {
  try {
    const d = await (await fetch('/api/config')).json();
    document.getElementById('romm-link').href = d.romm_url || `http://${HOST}:8086`;
    document.getElementById('gamevault-link').href = d.gamevault_url || `http://${HOST}:8087`;
  } catch(e) {}
}

async function loadPlatforms() {
  try {
    const d = await (await fetch('/api/platforms')).json();
    allPlatforms = d.platforms;
    const opts = allPlatforms.map(p => `<option value="${p.id}">${esc(p.name)}</option>`).join('');
    document.getElementById('platform-filter').innerHTML = opts;
    document.getElementById('lib-platform').innerHTML = opts;
    document.getElementById('wish-platform').innerHTML = allPlatforms.filter(p=>p.id!=='all').map(p => `<option value="${p.id}">${esc(p.name)}</option>`).join('');
  } catch(e) {}
}

async function doSearch() {
  const q = document.getElementById('search-input').value.trim();
  if (!q) return;
  const platform = document.getElementById('platform-filter').value;
  const btn = document.getElementById('search-btn');
  btn.disabled = true; btn.textContent = 'Searching...';
  document.getElementById('results').innerHTML = Array(6).fill('<div class="skeleton rounded-xl h-32"></div>').join('');
  document.getElementById('search-info').textContent = '';
  try {
    const d = await (await fetch(`/api/search?q=${encodeURIComponent(q)}&platform=${platform}`)).json();
    searchResults = d.results || [];
    document.getElementById('search-info').textContent = `${searchResults.length} results in ${d.search_time_ms}ms`;
    renderResults();
  } catch(e) { document.getElementById('search-info').textContent = 'Search failed'; toast('Search failed', 'error'); }
  finally { btn.disabled = false; btn.textContent = 'Search'; }
}

function renderResults() {
  const c = document.getElementById('results');
  if (!searchResults.length) { c.innerHTML = '<div class="col-span-full text-center py-16 text-slate-500"><div class="text-4xl mb-3">&#128270;</div>No results found</div>'; return; }
  c.innerHTML = searchResults.map((r, i) => {
    const score = r.safety_score || 50;
    const safetyClass = score >= 70 ? 'text-emerald-400 bg-emerald-400/10' : score >= 40 ? 'text-yellow-400 bg-yellow-400/10' : 'text-red-400 bg-red-400/10';
    const safetyLabel = score >= 70 ? 'Safe' : score >= 40 ? 'Caution' : 'Risky';
    const isDDL = r.source_type === 'ddl';
    const srcBadge = isDDL ? '<span class="px-1.5 py-0.5 text-xs rounded bg-purple-500/20 text-purple-400">DDL</span>' : '<span class="px-1.5 py-0.5 text-xs rounded bg-blue-500/20 text-blue-400">Torrent</span>';
    const platColor = r.is_pc ? 'bg-orange-500/20 text-orange-400' : 'bg-emerald-500/20 text-emerald-400';
    const warnings = (r.safety_warnings||[]).map(esc).join(' &middot; ');
    return `<div class="game-card bg-slate-900 border border-slate-800 rounded-xl p-4 ${score < 40 ? 'opacity-60' : ''}">
      <div class="flex items-start gap-3">
        <div class="flex flex-col items-center gap-1.5 pt-0.5 min-w-[60px]">
          <span class="px-2 py-0.5 text-xs font-bold rounded ${platColor}">${esc(r.platform)}</span>
          ${srcBadge}
          <span class="px-1.5 py-0.5 text-xs font-bold rounded ${safetyClass}">${safetyLabel}</span>
        </div>
        <div class="flex-1 min-w-0">
          <div class="text-sm font-medium text-white break-words leading-snug">${esc(r.title)}</div>
          <div class="flex flex-wrap gap-x-3 gap-y-1 mt-1.5 text-xs text-slate-400">
            <span>${esc(r.indexer)}</span>
            ${!isDDL ? `<span class="text-emerald-400">${r.seeders} seeds</span><span>${r.leechers} leech</span>` : '<span class="text-purple-400">Direct</span>'}
            <span>${r.size_human||'?'}</span>
          </div>
          ${warnings ? `<div class="text-xs text-red-400/70 mt-1">${warnings}</div>` : ''}
        </div>
        <button data-action="dlGame" data-idx="${i}" id="dl-btn-${i}" class="bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-semibold px-3 py-2 rounded-lg transition-colors whitespace-nowrap flex-shrink-0">DL</button>
      </div>
    </div>`;
  }).join('');
}

async function dlGame(idx) {
  const r = searchResults[idx]; const btn = document.getElementById('dl-btn-' + idx);
  btn.disabled = true; btn.textContent = '...';
  try {
    const d = await (await fetch('/api/download', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(r) })).json();
    if (d.success) { toast(`Downloading: ${r.title}`, 'success'); btn.textContent = 'Sent'; updateBadge(); }
    else { toast(d.error || 'Failed', 'error'); btn.disabled = false; btn.textContent = 'DL'; }
  } catch(e) { toast('Failed', 'error'); btn.disabled = false; btn.textContent = 'DL'; }
}

async function loadLibrary(page) {
  if (page) libPage = page;
  const q = document.getElementById('lib-search').value.trim();
  const plat = document.getElementById('lib-platform').value;
  try {
    const d = await (await fetch(`/api/library?page=${libPage}&q=${encodeURIComponent(q)}&platform=${plat}`)).json();
    document.getElementById('lib-stats').textContent = `${d.total} items (page ${d.page} of ${d.total_pages || 1})`;
    const grid = document.getElementById('library-grid');
    if (!d.items || !d.items.length) {
      grid.innerHTML = '<div class="col-span-full text-center py-16 text-slate-500"><div class="text-4xl mb-3">&#127918;</div>No games in library</div>';
    } else {
      const gradients = ['from-indigo-600 to-purple-600','from-emerald-600 to-teal-600','from-orange-600 to-red-600','from-pink-600 to-rose-600','from-cyan-600 to-blue-600','from-violet-600 to-fuchsia-600'];
      grid.innerHTML = d.items.map((item, i) => {
        const grad = gradients[i % gradients.length];
        const platColor = item.is_pc ? 'bg-orange-500/20 text-orange-400' : 'bg-emerald-500/20 text-emerald-400';
        const size = item.file_size > 0 ? formatSize(item.file_size) : '';
        return `<div class="game-card bg-slate-900 border border-slate-800 rounded-xl overflow-hidden">
          <div class="h-24 bg-gradient-to-br ${grad} flex items-center justify-center">
            <span class="text-3xl font-bold text-white/30">${esc((item.platform_slug || item.platform || '?')).toUpperCase().slice(0,4)}</span>
          </div>
          <div class="p-3">
            <div class="text-sm font-medium text-white truncate" title="${esc(item.title)}">${esc(item.title)}</div>
            <div class="flex items-center gap-2 mt-1.5">
              <span class="px-1.5 py-0.5 text-xs font-bold rounded ${platColor}">${esc(item.platform)}</span>
              ${size ? `<span class="text-xs text-slate-500">${size}</span>` : ''}
            </div>
          </div>
        </div>`;
      }).join('');
    }
    const pg = document.getElementById('lib-pagination');
    if (d.total_pages > 1) {
      let btns = '';
      if (d.page > 1) btns += `<button data-action="goLibraryPage" data-page="${d.page-1}" class="px-3 py-1 bg-slate-800 border border-slate-700 rounded text-sm hover:bg-slate-700">Prev</button>`;
      for (let p = Math.max(1,d.page-3); p <= Math.min(d.total_pages,d.page+3); p++)
        btns += `<button data-action="goLibraryPage" data-page="${p}" class="px-3 py-1 ${p===d.page ? 'bg-indigo-600 text-white' : 'bg-slate-800 border border-slate-700 hover:bg-slate-700'} rounded text-sm">${p}</button>`;
      if (d.page < d.total_pages) btns += `<button data-action="goLibraryPage" data-page="${d.page+1}" class="px-3 py-1 bg-slate-800 border border-slate-700 rounded text-sm hover:bg-slate-700">Next</button>`;
      pg.innerHTML = btns;
    } else pg.innerHTML = '';
  } catch(e) {}
}

async function pollDownloads() {
  try { const d = await (await fetch('/api/downloads')).json(); renderDownloads(d.downloads || []); } catch(e) {}
}
async function updateBadge() {
  try {
    const d = await (await fetch('/api/downloads')).json();
    const active = (d.downloads || []).filter(x => ['downloading','stalled','metadata','organizing','scanning','queued'].includes(x.status)).length;
    const badge = document.getElementById('dl-badge');
    if (active > 0) { badge.classList.remove('hidden'); badge.textContent = active; } else badge.classList.add('hidden');
  } catch(e) {}
}
function renderDownloads(downloads) {
  const c = document.getElementById('downloads');
  if (!downloads.length) { c.innerHTML = '<div class="text-center py-16 text-slate-500"><div class="text-4xl mb-3">&#128229;</div>No active downloads</div>'; return; }
  c.innerHTML = downloads.map(d => {
    const sc = {downloading:'bg-blue-500/20 text-blue-400',completed:'bg-emerald-500/20 text-emerald-400',error:'bg-red-500/20 text-red-400',organizing:'bg-yellow-500/20 text-yellow-400',scanning:'bg-purple-500/20 text-purple-400',dead_letter:'bg-red-500/20 text-red-300',interrupted:'bg-orange-500/20 text-orange-400'}[d.status] || 'bg-slate-700 text-slate-400';
    const hasProg = d.progress != null;
    const pctClass = d.progress >= 100 ? 'bg-emerald-500' : d.status === 'error' ? 'bg-red-500' : 'bg-indigo-500';
    const eta = d.eta > 0 && d.eta < 864000 ? (d.eta > 3600 ? `${Math.floor(d.eta/3600)}h ${Math.floor((d.eta%3600)/60)}m` : `${Math.floor(d.eta/60)}m`) : '';
    const hash = esc(d.hash || ''), jobId = esc(d.job_id || '');
    let actions = '';
    if (d.status === 'completed_unorganized' && d.hash) actions = `<button data-action="organizeTorrent" data-hash="${hash}" data-job-id="${jobId}" class="text-xs bg-indigo-600/20 text-indigo-400 px-2 py-1 rounded hover:bg-indigo-600/30">Organize</button>`;
    if (['error','interrupted','dead_letter'].includes(d.status) && d.job_id) actions += `<button data-action="retryJob" data-job-id="${jobId}" class="text-xs bg-yellow-600/20 text-yellow-400 px-2 py-1 rounded hover:bg-yellow-600/30">Retry</button>`;
    if (d.hash) actions += `<button data-action="removeDownload" data-hash="${hash}" data-job-id="${jobId}" class="text-xs text-slate-500 hover:text-red-400 px-2 py-1 rounded hover:bg-red-500/10">Remove</button>`;
    else if (d.job_id) actions += `<button data-action="removeJob" data-job-id="${jobId}" class="text-xs text-slate-500 hover:text-red-400 px-2 py-1 rounded hover:bg-red-500/10">Dismiss</button>`;
    return `<div class="bg-slate-900 border border-slate-800 rounded-xl p-4">
      <div class="flex items-center justify-between gap-3 mb-2"><span class="text-sm font-medium text-white break-words flex-1">${esc(d.title)}</span><div class="flex gap-1.5 flex-shrink-0">${actions}</div></div>
      <div class="flex flex-wrap gap-2 text-xs mb-2">
        <span class="px-2 py-0.5 rounded font-semibold ${sc}">${d.status}</span>
        ${d.platform ? `<span class="text-slate-500">${esc(d.platform)}</span>` : ''}
        ${d.size && d.size !== '?' ? `<span class="text-slate-500">${d.size}</span>` : ''}
        ${d.speed && !d.speed.startsWith('0') ? `<span class="text-slate-500">${d.speed}</span>` : ''}
        ${eta ? `<span class="text-slate-500">ETA: ${eta}</span>` : ''}
        ${hasProg ? `<span class="text-slate-400">${d.progress}%</span>` : ''}
      </div>
      ${hasProg ? `<div class="bg-slate-800 rounded-full h-1.5 overflow-hidden"><div class="progress-bar ${pctClass} h-full rounded-full" style="width:${d.progress}%"></div></div>` : ''}
      ${d.detail ? `<div class="text-xs text-slate-500 mt-1.5">${esc(d.detail)}</div>` : ''}
      ${d.error ? `<div class="text-xs text-red-400 mt-1">${esc(d.error)}</div>` : ''}
    </div>`;
  }).join('');
}
async function retryJob(id) { await fetch(`/api/downloads/${id}/retry`, {method:'POST'}); pollDownloads(); toast('Retrying...', 'success'); }
async function removeTorrent(h) { await fetch(`/api/downloads/torrent/${h}`, {method:'DELETE'}); pollDownloads(); }
async function removeJob(id) { await fetch(`/api/downloads/${id}`, {method:'DELETE'}); pollDownloads(); }
async function clearFinished() { await fetch('/api/downloads/clear', {method:'POST'}); pollDownloads(); toast('Cleared', 'success'); }
async function organizeTorrent(hash, jobId) {
  const plat = prompt('Platform? (pc, switch, ps2, ps3, psp, nds, 3ds, wii, ngc, dc, psx, gba, n64, snes, nes, gb, genesis, saturn, xbox, xbox360)', 'pc');
  if (!plat) return;
  const isPC = plat === 'pc';
  try {
    const d = await (await fetch(`/api/downloads/organize/${hash}`, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({platform: isPC ? 'PC' : plat.toUpperCase(), platform_slug: isPC ? '' : plat, is_pc: isPC})})).json();
    if (d.success) { if (jobId) removeJob(jobId); toast('Organizing...', 'success'); } else toast(d.error || 'Failed', 'error');
  } catch(e) { toast('Failed', 'error'); }
  pollDownloads();
}

async function loadWishlist() {
  try {
    const d = await (await fetch('/api/wishlist')).json();
    const items = d.items || [];
    const c = document.getElementById('wishlist');
    if (!items.length) { c.innerHTML = '<div class="text-center py-16 text-slate-500"><div class="text-4xl mb-3">&#10084;&#65039;</div>Wishlist is empty</div>'; return; }
    c.innerHTML = items.map(w => `<div class="bg-slate-900 border border-slate-800 rounded-xl p-4 flex items-center gap-3">
      <div class="flex-1"><div class="text-sm font-medium text-white">${esc(w.title)}</div><div class="text-xs text-slate-500 mt-0.5">${esc(w.platform||w.platform_slug||'')} &middot; ${(w.added_at||'').split('T')[0]||''}</div></div>
      <button data-action="wishSearch" data-title="${esc(w.title)}" class="text-xs bg-indigo-600/20 text-indigo-400 px-3 py-1.5 rounded-lg hover:bg-indigo-600/30">Search</button>
      <button data-action="deleteWishlist" data-id="${w.id}" class="text-xs text-slate-500 hover:text-red-400 px-2 py-1.5 rounded-lg hover:bg-red-500/10">Delete</button>
    </div>`).join('');
  } catch(e) {}
}
async function addWishlist() {
  const title = document.getElementById('wish-title').value.trim();
  const sel = document.getElementById('wish-platform');
  if (!title) { toast('Title required', 'error'); return; }
  await fetch('/api/wishlist', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({title, platform: sel.options[sel.selectedIndex].text, platform_slug: sel.value})});
  document.getElementById('wish-title').value = '';
  loadWishlist(); toast('Added to wishlist', 'success');
}
async function deleteWishlist(id) { await fetch(`/api/wishlist/${id}`, {method:'DELETE'}); loadWishlist(); }
function wishSearch(title) { document.getElementById('search-input').value = title; switchTab('search'); doSearch(); }

async function loadSettings() { try { const d = await (await fetch('/api/settings')).json(); document.getElementById('setting-extract').checked = !!d.extract_archives; } catch(e) {} }
async function saveSetting(key, value) { await fetch('/api/settings', {method:'PUT', headers:{'Content-Type':'application/json'}, body: JSON.stringify({[key]: value})}); }
async function loadSources() {
  try {
    const d = await (await fetch('/api/sources')).json();
    document.getElementById('settings-sources').innerHTML = (d.sources||[]).map(s => {
      const dot = s.enabled ? 'bg-emerald-500' : 'bg-slate-600';
      const srcColor = s.source_type === 'torrent' ? 'bg-blue-500/20 text-blue-400' : 'bg-purple-500/20 text-purple-400';
      return `<div class="flex items-center justify-between bg-slate-800 rounded-lg p-3"><div class="flex items-center gap-3"><span class="w-2 h-2 rounded-full ${dot}"></span><span class="text-sm text-white">${esc(s.label)}</span></div><span class="px-2 py-0.5 text-xs rounded ${srcColor}">${s.source_type}</span></div>`;
    }).join('');
  } catch(e) {}
}
async function loadStats() {
  try {
    const d = await (await fetch('/api/stats')).json();
    const plats = d.platforms || {};
    const maxVal = Math.max(1, ...Object.values(plats));
    document.getElementById('header-stats').textContent = `${d.library_total || 0} games`;
    const bars = Object.entries(plats).sort((a,b) => b[1]-a[1]).map(([slug, count]) => `<div class="mb-2"><div class="flex justify-between text-xs mb-0.5"><span class="text-slate-300">${esc(slug)}</span><span class="text-slate-500">${count}</span></div><div class="bg-slate-800 rounded-full h-1.5"><div class="bg-indigo-500 rounded-full h-1.5" style="width:${Math.round(count/maxVal*100)}%"></div></div></div>`).join('') || '<div class="text-sm text-slate-500">No library data yet</div>';
    document.getElementById('settings-stats').innerHTML = `<div class="grid grid-cols-2 gap-3 mb-4">
      <div class="bg-slate-800 rounded-lg p-3 text-center"><div class="text-2xl font-bold text-indigo-400">${d.library_total||0}</div><div class="text-xs text-slate-500 mt-1">Library Items</div></div>
      <div class="bg-slate-800 rounded-lg p-3 text-center"><div class="text-2xl font-bold text-slate-300">${d.total_jobs||0}</div><div class="text-xs text-slate-500 mt-1">Total Jobs</div></div>
    </div>` + bars;
  } catch(e) {}
}
async function loadActivity() {
  try {
    const d = await (await fetch('/api/activity')).json();
    const entries = d.entries || [];
    const c = document.getElementById('activity-log');
    if (!entries.length) { c.innerHTML = '<div class="text-sm text-slate-500">No activity yet</div>'; return; }
    c.innerHTML = entries.slice(0, 20).map(e => {
      const col = {download_started:'text-blue-400',download_completed:'text-emerald-400',download_failed:'text-red-400',import_completed:'text-purple-400',download_retried:'text-yellow-400'}[e.event_type] || 'text-slate-400';
      return `<div class="flex gap-2 py-1.5 border-b border-slate-800/50 text-xs"><span class="text-slate-600 flex-shrink-0">${(e.timestamp||'').split('T')[1]?.slice(0,5)||''}</span><span class="${col} font-medium flex-shrink-0">${esc(e.event_type)}</span><span class="text-slate-400 truncate">${esc(e.title)}</span></div>`;
    }).join('');
  } catch(e) {}
}
async function loadMonitor() {
  try {
    const d = await (await fetch('/api/monitor/status')).json();
    const c = document.getElementById('monitor-info');
    const dotColor = d.enabled ? 'bg-emerald-500' : 'bg-slate-600';
    const statusText = d.enabled ? `Active &middot; ${esc(d.provider)} (${esc(d.model)})` : 'Disabled';
    c.innerHTML = `<div class="flex items-center gap-2"><span class="w-2 h-2 rounded-full ${dotColor}"></span><span class="text-sm text-slate-300">${statusText}</span></div>
      <div class="text-sm text-slate-400 bg-slate-800 rounded-lg p-3">${esc(d.diagnosis || '—')}</div>`;
  } catch(e) {}
}
async function triggerAnalysis() { await fetch('/api/monitor/analyze', {method:'POST'}); setTimeout(loadMonitor, 500); toast('Analysis triggered', 'success'); }
async function testConn(service) {
  const el = document.getElementById(`test-${service}-status`);
  el.textContent = 'Testing...'; el.className = 'text-xs text-yellow-400 mt-1';
  try {
    const d = await (await fetch(`/api/test/${service}`, {method:'POST'})).json();
    if (d.success) { el.textContent = 'Connected'; el.className = 'text-xs text-emerald-400 mt-1'; }
    else { el.textContent = d.error || 'Failed'; el.className = 'text-xs text-red-400 mt-1'; }
  } catch(e) { el.textContent = 'Error'; el.className = 'text-xs text-red-400 mt-1'; }
}

function toast(msg, type) {
  const c = document.getElementById('toast-container');
  const colors = {success:'border-l-emerald-500', error:'border-l-red-500'};
  const el = document.createElement('div');
  el.className = `toast-enter bg-slate-900 border border-slate-700 border-l-4 ${colors[type]||'border-l-slate-500'} rounded-lg p-3 text-sm text-slate-200 shadow-xl`;
  el.textContent = msg; c.appendChild(el); setTimeout(() => el.remove(), 4000);
}
function toggleMobileNav() {
  document.getElementById('main-nav').classList.toggle('open');
  document.getElementById('hamburger-btn').classList.toggle('active');
  document.getElementById('nav-overlay').classList.toggle('open');
}
function closeMobileNav() {
  document.getElementById('main-nav').classList.remove('open');
  document.getElementById('hamburger-btn').classList.remove('active');
  document.getElementById('nav-overlay').classList.remove('open');
}
// esc() escapes text for safe interpolation into HTML — including quotes, so
// escaped values are also safe inside double-quoted attributes (data-*, title).
function esc(s) { if (!s) return ''; return String(s).replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch])); }
function formatSize(bytes) { if (!bytes) return ''; const u = ['B','KB','MB','GB','TB']; let i = 0, s = bytes; while (s >= 1024 && i < u.length-1) { s /= 1024; i++; } return s.toFixed(1) + ' ' + u[i]; }

// ============================================================
// EVENT DELEGATION
// ============================================================
// Replaces all inline on*= attributes so the UI runs under a strict
// Content-Security-Policy (script-src 'self'). Markup declares intent via
// data-action="..."; this explicit whitelist maps it to code — markup can
// never invoke anything that isn't registered here.
const CLICK_ACTIONS = {
  // Static chrome:
  switchTab: el => switchTab(el.dataset.tab),
  toggleMobileNav: () => toggleMobileNav(),
  doSearch: () => doSearch(),
  clearFinished: () => clearFinished(),
  addWishlist: () => addWishlist(),
  triggerAnalysis: () => triggerAnalysis(),
  testConn: el => testConn(el.dataset.service),
  // Dynamically rendered rows/cards:
  dlGame: el => dlGame(+el.dataset.idx),
  goLibraryPage: el => loadLibrary(+el.dataset.page),
  organizeTorrent: el => organizeTorrent(el.dataset.hash, el.dataset.jobId || ''),
  retryJob: el => retryJob(el.dataset.jobId),
  removeJob: el => removeJob(el.dataset.jobId),
  removeDownload: el => { removeTorrent(el.dataset.hash); if (el.dataset.jobId) removeJob(el.dataset.jobId); },
  wishSearch: el => wishSearch(el.dataset.title),
  deleteWishlist: el => deleteWishlist(+el.dataset.id),
};

document.addEventListener('click', e => {
  const el = e.target.closest('[data-action]');
  if (!el) return;
  const fn = CLICK_ACTIONS[el.dataset.action];
  if (!fn) return;
  // Anchors previously used inline handlers — keep them from navigating.
  if (el.tagName === 'A') e.preventDefault();
  fn(el, e);
});

const CHANGE_ACTIONS = {
  loadLibrary: () => loadLibrary(),
  saveExtractSetting: el => saveSetting('extract_archives', el.checked),
};

document.addEventListener('change', e => {
  const el = e.target.closest('[data-action-change]');
  if (!el) return;
  const fn = CHANGE_ACTIONS[el.dataset.actionChange];
  if (fn) fn(el, e);
});

// Enter-to-submit on the search boxes (replaces inline onkeydown=).
document.getElementById('search-input').addEventListener('keydown', e => { if (e.key === 'Enter') doSearch(); });
document.getElementById('lib-search').addEventListener('keydown', e => { if (e.key === 'Enter') loadLibrary(); });
