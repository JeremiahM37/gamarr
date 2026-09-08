# Minerva Source Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional multi-platform Minerva Archive source to Gamarr with an incrementally maintained local SQLite index and qBittorrent selective-file downloads.

**Architecture:** Gamarr builds its searchable Minerva index from the real Minerva `.torrent` metadata, stores it at `DATA_DIR/minerva/index.db`, and searches locally. A Minerva result is a torrent result plus an exact target file index/path/size; qBittorrent validates the live file list before Gamarr changes priorities or starts payload transfer. Scheduled sync first checks lightweight assets metadata and only downloads/parses collection torrents that are new or changed.

**Tech Stack:** Go 1.24+, `modernc.org/sqlite` already present in Gamarr, `net/http`, `crypto/sha1`, existing qBittorrent Web API client, chi HTTP API, existing Gamarr search/download/health pipelines.

**Spec:** `docs/superpowers/specs/2026-09-08-minerva-source-design.md`

## Global Constraints

- Minerva is optional and disabled by default.
- Support is multi-platform only for platform slugs Gamarr already supports; do not add new Gamarr platforms solely for Minerva.
- Minerva does not replace Myrient, Vimm, or Prowlarr.
- qBittorrent is the only Minerva payload downloader in v1; no Transmission, Deluge, SABnzbd, NZBGet, or aria2c Minerva download path.
- Do not add aria2c solely to build the index; parse `.torrent` metadata natively in Go.
- Do not bundle a large static Minerva file database in Git.
- Do not rely on a third-party Minerva file index as the primary source of truth.
- Validate the indexed target against qBittorrent's real file list before payload download starts.
- Preserve the last usable local index when a sync fails.
- Scheduled sync interval defaults to 24 hours and must avoid reparsing unchanged torrents.
- All existing Myrient/Vimm/Prowlarr behavior must remain backward compatible.

---

## File Structure

```text
internal/minerva/
├── bencode.go       # bounded decoder + raw info-dictionary byte range
├── torrent.go       # .torrent -> TorrentMeta/FileMeta + v1 info hash
├── index.go         # SQLite schema, atomic collection replacement, local search
├── client.go        # Minerva assets/version and conditional torrent HTTP requests
├── sync.go          # incremental/full sync + concurrency state
└── *_test.go
```

Existing files changed:

```text
internal/sources/sources.go
internal/sources/defaults.json
internal/sources/load.go
internal/sources/sources_test.go
internal/config/config_test.go
internal/models/models.go
internal/qbit/client.go
internal/qbit/client_test.go
internal/download/manager.go
internal/download/manager_test.go
internal/search/minerva.go
internal/search/minerva_test.go
internal/api/api.go
internal/api/requests.go
internal/api/torznab_wire.go
internal/api/minerva.go
internal/api/main_test.go
internal/api/router_test.go
internal/api/openapi.json
internal/api/admin.go
cmd/gamarr/main.go
README.md
```

---

### Task 1: Add Backward-Compatible Minerva Source Configuration

**Files:**
- Modify: `internal/sources/sources.go`
- Modify: `internal/sources/defaults.json`
- Modify: `internal/sources/load.go`
- Modify: `internal/sources/sources_test.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**

```go
type MinervaSpec struct {
    Enabled           bool              `json:"enabled"`
    BaseURL           string            `json:"base_url"`
    AssetsURL         string            `json:"assets_url"`
    SyncIntervalHours int               `json:"sync_interval_hours"`
    PlatformPaths     map[string]string `json:"platform_paths"`
}
```

`Registry` gains `Minerva MinervaSpec`. Env overrides are `MINERVA_ENABLED`, `MINERVA_URL`, `MINERVA_ASSETS_URL`, and `MINERVA_SYNC_INTERVAL_HOURS`.

- [ ] **Step 1: Write failing default/override tests**

Add to `internal/sources/sources_test.go`:

```go
func TestDefault_MinervaDisabledAndConfigured(t *testing.T) {
    r, err := Default()
    if err != nil { t.Fatal(err) }
    if r.Minerva.Enabled { t.Fatal("Minerva must be disabled by default") }
    if r.Minerva.BaseURL != "https://minerva-archive.org/" { t.Fatalf("BaseURL=%q", r.Minerva.BaseURL) }
    if r.Minerva.AssetsURL != "https://minerva-archive.org/assets/" { t.Fatalf("AssetsURL=%q", r.Minerva.AssetsURL) }
    if r.Minerva.SyncIntervalHours != 24 { t.Fatalf("SyncIntervalHours=%d", r.Minerva.SyncIntervalHours) }
    if r.Minerva.PlatformPaths["nds"] != "No-Intro/Nintendo - Nintendo DS (Decrypted)/" {
        t.Fatalf("nds path=%q", r.Minerva.PlatformPaths["nds"])
    }
}
```

Extend the existing env-override test to set all four Minerva variables and assert the resulting values.

- [ ] **Step 2: Verify the tests fail**

```bash
go test ./internal/sources -run 'TestDefault_Minerva|TestApplyEnvOverrides' -v
```

Expected: compile failure because Minerva config types do not exist.

- [ ] **Step 3: Add disabled embedded defaults**

Add to `defaults.json`:

```json
"minerva": {
  "enabled": false,
  "base_url": "https://minerva-archive.org/",
  "assets_url": "https://minerva-archive.org/assets/",
  "sync_interval_hours": 24,
  "platform_paths": {
    "gba": "No-Intro/Nintendo - Game Boy Advance/",
    "gb": "No-Intro/Nintendo - Game Boy/",
    "gbc": "No-Intro/Nintendo - Game Boy Color/",
    "nes": "No-Intro/Nintendo - Nintendo Entertainment System (Headered)/",
    "snes": "No-Intro/Nintendo - Super Nintendo Entertainment System/",
    "n64": "No-Intro/Nintendo - Nintendo 64 (BigEndian)/",
    "nds": "No-Intro/Nintendo - Nintendo DS (Decrypted)/",
    "3ds": "No-Intro/Nintendo - Nintendo 3DS (Decrypted)/",
    "psx": "Redump/Sony - PlayStation/",
    "ps2": "Redump/Sony - PlayStation 2/",
    "ps3": "Redump/Sony - PlayStation 3/",
    "psp": "Redump/Sony - PlayStation Portable/",
    "dc": "Redump/Sega - Dreamcast/",
    "saturn": "Redump/Sega - Saturn/",
    "genesis": "No-Intro/Sega - Mega Drive - Genesis/",
    "ngc": "Redump/Nintendo - GameCube - NKit RVZ [zstd-19-128k]/",
    "wii": "Redump/Nintendo - Wii - NKit RVZ [zstd-19-128k]/",
    "xbox": "Redump/Microsoft - Xbox/",
    "xbox360": "Redump/Microsoft - Xbox 360/"
  }
}
```

A 404 for one configured Minerva collection later means “platform unavailable from Minerva”, not startup failure.

- [ ] **Step 4: Implement env overrides**

`ApplyEnvOverrides` changes Minerva values only when the env variable is present/non-empty. Accept `true/false`, `1/0`, `yes/no` for `MINERVA_ENABLED`; invalid values leave the registry value unchanged. Accept only a positive integer for the sync interval. An external registry omitting `minerva` remains valid and disabled.

- [ ] **Step 5: Assert disabled-by-default through `config.Load`**

Add Minerva env names to the cleanup list in `internal/config/config_test.go`, then assert:

```go
if cfg.Sources.Minerva.Enabled {
    t.Fatal("Minerva must be disabled unless explicitly enabled")
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/sources ./internal/config -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/sources internal/config/config_test.go
git commit -m "feat: add Minerva source configuration"
```

---

### Task 2: Parse BitTorrent Metadata Natively and Safely

**Files:**
- Create: `internal/minerva/bencode.go`
- Create: `internal/minerva/torrent.go`
- Create: `internal/minerva/torrent_test.go`

**Interfaces:**

```go
type FileMeta struct {
    Index int
    Path  string
    Name  string
    Size  int64
}

type TorrentMeta struct {
    Name     string
    InfoHash string
    Files    []FileMeta
}

func ParseTorrent(data []byte) (TorrentMeta, error)
```

- [ ] **Step 1: Write failing parser tests**

Cover one single-file torrent, one multi-file torrent, and rejected path/malformed cases. The multi-file assertion must verify file order and index `0..n-1`, because qBittorrent uses this order.

```go
func TestParseTorrentMultiFile(t *testing.T) {
    data := []byte("d4:infod5:filesld6:lengthi3e4:pathl5:a.ndseed6:lengthi4e4:pathl3:dir5:b.ndseee4:name10:collectionee")
    got, err := ParseTorrent(data)
    if err != nil { t.Fatal(err) }
    if got.Name != "collection" || len(got.Files) != 2 { t.Fatalf("got %+v", got) }
    if got.Files[0].Index != 0 || got.Files[0].Path != "a.nds" || got.Files[0].Size != 3 { t.Fatalf("first=%+v", got.Files[0]) }
    if got.Files[1].Index != 1 || got.Files[1].Path != "dir/b.nds" || got.Files[1].Size != 4 { t.Fatalf("second=%+v", got.Files[1]) }
    if len(got.InfoHash) != 40 { t.Fatalf("info hash=%q", got.InfoHash) }
}
```

Reject `../escape.nds`, absolute paths, NUL bytes, empty filenames, malformed integers/lists/dictionaries, and missing/duplicate top-level `info` keys.

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/minerva -run TestParseTorrent -v
```

Expected: compile failure because `ParseTorrent` does not exist.

- [ ] **Step 3: Implement the bounded bencode decoder**

Decode integers, byte strings, lists, and dictionaries with a maximum nesting depth of 64 and no reads beyond the supplied byte slice. While parsing the top-level dictionary, record the exact start/end byte offsets of the `info` value. Keep decoder types unexported.

- [ ] **Step 4: Implement `ParseTorrent`**

Support BitTorrent v1 `length` and `files`. Compute the hash from the original raw info bytes:

```go
sum := sha1.Sum(infoRaw)
infoHash := hex.EncodeToString(sum[:])
```

Normalize with `path.Clean`; reject `.`/empty, leading `/`, any cleaned path beginning `../`, and NUL bytes. Set `Name` to `path.Base(cleanPath)` and preserve the torrent file list order.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/minerva -run TestParseTorrent -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/minerva/bencode.go internal/minerva/torrent.go internal/minerva/torrent_test.go
git commit -m "feat: parse Minerva torrent metadata"
```

---

### Task 3: Add the Local SQLite Index and Search

**Files:**
- Create: `internal/minerva/index.go`
- Create: `internal/minerva/index_test.go`

**Interfaces:**

```go
type IndexedFile struct {
    PlatformSlug string
    Name         string
    Path         string
    Size         int64
    FileIndex    int
    TorrentURL   string
    InfoHash     string
}

type CollectionRecord struct {
    PlatformSlug  string
    BrowsePath    string
    BundleVersion string
    TorrentURL    string
    InfoHash      string
    ETag          string
    LastModified  string
    ContentSHA256 string
}

type Index struct {
    db *sql.DB
}

func OpenIndex(dbPath string) (*Index, error)
func (i *Index) Close() error
func (i *Index) ReplaceCollection(ctx context.Context, rec CollectionRecord, files []FileMeta) error
func (i *Index) UpdateValidators(ctx context.Context, platformSlug, etag, lastModified string) error
func (i *Index) Search(ctx context.Context, query, platformSlug string, limit int) ([]IndexedFile, error)
func (i *Index) Collection(ctx context.Context, platformSlug string) (CollectionRecord, bool, error)
func (i *Index) SetState(ctx context.Context, key, value string) error
func (i *Index) State(ctx context.Context, key string) (string, bool, error)
func (i *Index) Counts(ctx context.Context) (collections, files int, err error)
```

- [ ] **Step 1: Write failing index tests**

Create a temp DB, insert one NDS collection, search `pokemon heartgold`, and assert the result includes target file index, hash, URL and size. Add tests for platform isolation, result limit, and atomic replacement (old file rows remain if a replacement insertion fails).

```go
hits, err := idx.Search(context.Background(), "pokemon heartgold", "nds", 20)
if err != nil { t.Fatal(err) }
if len(hits) != 1 || hits[0].FileIndex != 7 || hits[0].InfoHash != strings.Repeat("a", 40) {
    t.Fatalf("hits=%+v", hits)
}
```

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/minerva -run 'TestIndex|TestSearch' -v
```

Expected: compile failure because `OpenIndex` does not exist.

- [ ] **Step 3: Implement schema creation**

Create these tables/indexes and enable foreign keys:

```sql
CREATE TABLE IF NOT EXISTS minerva_collections (
  platform_slug TEXT PRIMARY KEY,
  browse_path TEXT NOT NULL,
  bundle_version TEXT NOT NULL,
  torrent_url TEXT NOT NULL,
  info_hash TEXT NOT NULL,
  etag TEXT NOT NULL DEFAULT '',
  last_modified TEXT NOT NULL DEFAULT '',
  content_sha256 TEXT NOT NULL,
  indexed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS minerva_files (
  platform_slug TEXT NOT NULL REFERENCES minerva_collections(platform_slug) ON DELETE CASCADE,
  file_index INTEGER NOT NULL,
  path TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  PRIMARY KEY(platform_slug, file_index)
);
CREATE INDEX IF NOT EXISTS idx_minerva_files_platform_name ON minerva_files(platform_slug, name);
CREATE TABLE IF NOT EXISTS minerva_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

- [ ] **Step 4: Implement atomic replacement and local search**

`ReplaceCollection` runs the collection upsert, old-file delete, and new-file inserts in one transaction. `Search` lowercases/tokenizes whitespace-delimited query words and builds parameterized `LOWER(name) LIKE ?` predicates joined by `AND`. Require a non-empty platform slug in v1 and cap limit to `1..100` with default 20.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/minerva -run 'TestIndex|TestSearch' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/minerva/index.go internal/minerva/index_test.go
git commit -m "feat: add local Minerva search index"
```

---

### Task 4: Implement Incremental Sync and Atomic Async Start

**Files:**
- Create: `internal/minerva/client.go`
- Create: `internal/minerva/sync.go`
- Create: `internal/minerva/sync_test.go`

**Interfaces:**

```go
var ErrSyncInProgress = errors.New("minerva sync already in progress")

type SyncReport struct {
    Checked   int `json:"checked"`
    Updated   int `json:"updated"`
    Unchanged int `json:"unchanged"`
    Missing   int `json:"missing"`
    Files     int `json:"files"`
}

type Status struct {
    Enabled     bool      `json:"enabled"`
    Ready       bool      `json:"ready"`
    Syncing     bool      `json:"syncing"`
    LastSync    time.Time `json:"last_sync,omitempty"`
    LastError   string    `json:"last_error,omitempty"`
    Collections int       `json:"collections"`
    Files       int       `json:"files"`
}

type Service struct {
    spec      sources.MinervaSpec
    client    *http.Client
    index     *Index
    mu        sync.Mutex
    syncing   bool
    lastError string
}

func Open(dataDir string, spec sources.MinervaSpec) (*Service, error)
func (s *Service) Close() error
func (s *Service) Sync(ctx context.Context, force bool) (SyncReport, error)
func (s *Service) StartSync(ctx context.Context, force bool) error
func (s *Service) Search(ctx context.Context, query, platformSlug string, limit int) ([]IndexedFile, error)
func (s *Service) Status(ctx context.Context) Status
func (s *Service) Ready(ctx context.Context) bool
```

`Sync` is blocking. `StartSync` atomically claims the same `syncing` guard, then launches the actual sync in a goroutine and returns immediately. Both call one private `runSync` implementation; both clear the guard and set `lastError` exactly once when finished.

- [ ] **Step 1: Write failing incremental/concurrency tests**

Use an `httptest.Server` that serves an assets listing and two fake torrents. Assert:

```text
first Sync: assets 200, torrent metadata fetched and indexed
second Sync: assets request sends validators and receives 304, zero torrent requests
changed assets response: unchanged collection 304, changed collection 200/replaced
StartSync: first call nil, immediate second call ErrSyncInProgress
```

Also assert a 404 or malformed changed torrent never deletes the previously good rows.

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/minerva -run TestSync -v
```

Expected: compile failure because `Service` does not exist.

- [ ] **Step 3: Implement assets bundle discovery**

GET `spec.AssetsURL` with `User-Agent: Gamarr/1.0`. Parse directory links named `Minerva_Myrient_v<major>.<minor>` and select the highest numeric pair. Persist these state keys:

```text
assets_etag
assets_last_modified
assets_body_sha256
bundle_version
last_sync
```

On non-forced sync, send `If-None-Match` and `If-Modified-Since`. `304` returns immediately. A `200` whose SHA-256 and selected bundle version both equal stored values also returns immediately without requesting collection torrents.

- [ ] **Step 4: Implement deterministic torrent URLs**

For a platform browse path, trim its trailing slash, replace `/` with ` - `, create filename `Minerva_Myrient - <collection-name>.torrent`, URL-escape that filename, and join it under:

```text
<AssetsURL>/Minerva_Myrient_<bundle-version>/
```

Keep both endpoint and collection paths overrideable via `MinervaSpec`.

- [ ] **Step 5: Implement per-collection conditional sync**

For existing rows send stored ETag/Last-Modified. Handle exactly:

```text
304 -> Unchanged++, no parse
404 -> Missing++, preserve previous row/files
200 -> read through io.LimitReader capped at 256 MiB + 1 byte; reject overflow
200 same SHA-256 -> UpdateValidators, Unchanged++
200 changed/new -> ParseTorrent + ReplaceCollection, Updated++, Files += parsed file count
other status/network/parse error -> return error, leave old collection untouched
```

A forced sync bypasses validators/body-equality and reparses each reachable configured collection; it does not clear the DB first.

- [ ] **Step 6: Implement the shared concurrency guard**

Create private methods:

```go
func (s *Service) beginSync() error
func (s *Service) finishSync(err error)
func (s *Service) runSync(ctx context.Context, force bool) (SyncReport, error)
```

`Sync` calls `beginSync`, defers `finishSync`, then calls `runSync`. `StartSync` calls `beginSync`, starts one goroutine that calls `runSync` and `finishSync`, then returns. This makes the HTTP 202/409 decision race-free.

- [ ] **Step 7: Run all Minerva tests**

```bash
go test ./internal/minerva -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/minerva/client.go internal/minerva/sync.go internal/minerva/sync_test.go
git commit -m "feat: sync Minerva index incrementally"
```

---

### Task 5: Add qBittorrent Selective-File Primitives

**Files:**
- Modify: `internal/qbit/client.go`
- Modify: `internal/qbit/client_test.go`

**Interfaces:**

```go
type TorrentFile struct {
    Name     string  `json:"name"`
    Size     int64   `json:"size"`
    Priority int     `json:"priority"`
    Index    int     `json:"index"`
    Progress float64 `json:"progress"`
}

func (c *Client) AddTorrentPaused(torrentURL, title, savePath, category string) bool
func (c *Client) SetFilePriority(hash string, ids []int, priority int) bool
func (c *Client) StartTorrent(hash string) bool
```

- [ ] **Step 1: Write failing API-contract tests**

Assert the fake qB server receives:

```text
POST /api/v2/torrents/add       urls/savepath/category plus non-starting add flags
POST /api/v2/torrents/filePrio  hash, id="0|1|2", priority="0"
POST /api/v2/torrents/start     hashes=<hash>
```

Also test `/start` 404 fallback to `/resume`.

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/qbit -run 'TestAddTorrentPaused|TestSetFilePriority|TestStartTorrent' -v
```

Expected: compile failure because the new methods do not exist.

- [ ] **Step 3: Implement paused add without changing existing add semantics**

Keep `AddTorrent` unchanged. `AddTorrentPaused` uses the same authentication/response handling but adds qB's non-starting add fields (`stopped=true` and legacy-compatible `paused=true`). Preserve existing qB 5.2 JSON/204 tests.

- [ ] **Step 4: Implement file priority and start/resume**

`SetFilePriority` rejects an empty id slice, joins ids with `|`, posts `/api/v2/torrents/filePrio`, and reauthenticates on 403 like existing mutators. `StartTorrent` posts `/api/v2/torrents/start` and falls back to `/api/v2/torrents/resume` on 404. Accept any 2xx as success.

- [ ] **Step 5: Run qB tests**

```bash
go test ./internal/qbit -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/qbit/client.go internal/qbit/client_test.go
git commit -m "feat: add qBittorrent selective file controls"
```

---

### Task 6: Add Safe Selective Download and Target-Only Import

**Files:**
- Modify: `internal/download/manager.go`
- Modify: `internal/download/manager_test.go`

**Interfaces:**

```go
func (m *Manager) DownloadSelectiveTorrent(
    url, infoHash string,
    fileIndex int,
    filePath string,
    fileSize int64,
    title, platf, platSlug string,
    isPC bool,
) (string, error)
```

Selective jobs persist `source=minerva`, `download_url`, `info_hash`, `torrent_file_index`, `torrent_file_path`, `torrent_file_size`, title/platform fields for retry.

- [ ] **Step 1: Write failing mismatch-safety test**

Fake qB returns index 7 as `Different.nds` while the request expects `HeartGold.nds`. Assert job error, zero `filePrio` calls, zero start/resume calls, and no library import.

- [ ] **Step 2: Write failing successful-selection test**

For a new 3-file torrent assert order:

```text
add paused -> GetTorrentFiles -> all indices priority 0 -> target priority 7 -> start
```

Then make only the target report `progress=1` and assert only that target is imported.

- [ ] **Step 3: Verify failure**

```bash
go test ./internal/download -run TestDownloadSelectiveTorrent -v
```

Expected: compile failure because the method does not exist.

- [ ] **Step 4: Implement new-vs-existing torrent behavior**

Before add, query qB torrents and match the normalized lowercase hash.

```text
new hash: add paused; after validation set all priorities 0, target 7, start
existing hash: do not zero existing wanted files; after validation set target 7, start only if stopped
```

This supports concurrent requests for two files in the same Minerva collection without cancelling the first target.

- [ ] **Step 5: Validate the live target before priorities**

Poll `GetTorrentFiles(infoHash)` for at most 30 seconds. Find exact `Index`, normalize both paths to slash-separated relative clean paths, require exact path equality, and require exact size when `fileSize > 0`. A mismatch fails before priority/start. Delete the torrent/data only if this invocation created it; never delete a pre-existing torrent. Record `search.RecordDownloadFail("minerva", reason)`.

- [ ] **Step 6: Watch only the requested file**

Poll every 5 seconds and finish when the selected `TorrentFile.Progress >= 1`. Resolve the source from `Torrent.SavePath` plus the qB-returned relative file path; reject any cleaned path that escapes `SavePath`.

- [ ] **Step 7: Scan and import one file through existing import modes**

Run the same ClamAV single-path scan used by DDL. Import only the target into `GAMES_ROMS_PATH/<platform_slug>/` using the existing `importContent` method so move/hardlink/symlink/copy behavior stays consistent. Add library metadata with source `minerva`, write the existing sidecar format with source `minerva`, mark job complete, and call `RecordDownloadSuccess("minerva")`.

- [ ] **Step 8: Add retry dispatch**

A failed selective job retries only when URL, hash, file index/path/size and platform fields are present. Reinvoke `DownloadSelectiveTorrent`; do not reinterpret it as a generic torrent.

- [ ] **Step 9: Run download tests**

```bash
go test ./internal/download -v
```

Expected: PASS, including mismatch-before-start, pre-existing priority preservation, target-only import, and retry.

- [ ] **Step 10: Commit**

```bash
git add internal/download/manager.go internal/download/manager_test.go
git commit -m "feat: download selected Minerva torrent files"
```

---

### Task 7: Integrate Minerva Search Results and Selective Routing

**Files:**
- Modify: `internal/models/models.go`
- Create: `internal/search/minerva.go`
- Create: `internal/search/minerva_test.go`
- Modify: `internal/api/api.go`
- Modify: `internal/api/requests.go`
- Modify: `internal/api/torznab_wire.go`
- Modify: `cmd/gamarr/main.go`

**Interfaces:**

Add to both `models.SearchResult` and `models.DownloadRequest`:

```go
TorrentFileIndex *int   `json:"torrent_file_index,omitempty"`
TorrentFilePath  string `json:"torrent_file_path,omitempty"`
TorrentFileSize  int64  `json:"torrent_file_size,omitempty"`
```

Produce:

```go
func SearchMinerva(svc *minerva.Service, query, platformSlug string) []*models.SearchResult
```

- [ ] **Step 1: Write failing result-mapping test**

Seed a temp Minerva index and assert a HeartGold hit maps to `Indexer="Minerva"`, `SourceType="torrent"`, `DownloadProtocol="torrent"`, selected ROM size, info hash/torrent URL, safety score 95, and non-nil file index 7.

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/search -run TestSearchMinerva -v
```

Expected: compile failure because fields/adapter do not exist.

- [ ] **Step 3: Implement the adapter**

`SearchMinerva` returns nil when service is nil/not ready, platform is empty/`all`, or Minerva circuit is open. A successful local DB query calls `RecordSearchSuccess("minerva")`; DB error calls `RecordSearchFail`. Map selected file size (not collection size) and leave seeders at zero in v1 to avoid adding a live network dependency to every search.

- [ ] **Step 4: Route selective downloads before generic torrent routing**

In `/api/download` and `/api/requests/{id}/download`, `TorrentFileIndex != nil` requires URL, info hash and file path and calls `DownloadSelectiveTorrent`. It must never fall through to `DownloadTorrent`, because that path can use non-qB clients and whole-torrent organization.

- [ ] **Step 5: Add Minerva to all four search fan-outs**

Add a conditional Minerva search call to:

```text
/api/search                     internal/api/api.go
request search                  internal/api/requests.go
Torznab search                  internal/api/torznab_wire.go
scheduler searchFn              cmd/gamarr/main.go
```

Only add the extra goroutine/WaitGroup count when a non-nil enabled service is wired. Disabled Minerva must perform no Minerva DB/network work.

- [ ] **Step 6: Run search/API tests**

```bash
go test ./internal/search ./internal/api ./cmd/gamarr -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/models/models.go internal/search/minerva.go internal/search/minerva_test.go internal/api/api.go internal/api/requests.go internal/api/torznab_wire.go cmd/gamarr/main.go
git commit -m "feat: integrate Minerva search results"
```

---

### Task 8: Wire Lifecycle, Periodic Sync, Status, and Manual Sync API

**Files:**
- Modify: `cmd/gamarr/main.go`
- Modify: `internal/api/api.go`
- Create: `internal/api/minerva.go`
- Modify: `internal/api/main_test.go`
- Modify: `internal/api/router_test.go`
- Modify: `internal/api/admin.go`

**Interfaces:**

`api.Server` gains `minerva *minerva.Service` and router signature becomes:

```go
func NewRouter(
    cfg *config.Config,
    mgr *download.Manager,
    mon *monitor.GamarrMonitor,
    sab *sabnzbd.Client,
    sched *scheduler.Scheduler,
    minervaSvc *minerva.Service,
) http.Handler
```

Routes:

```text
GET  /api/minerva/status
POST /api/minerva/sync       admin only, optional JSON {"full":false}
```

- [ ] **Step 1: Write failing route tests**

Disabled status returns exactly an enabled/ready/syncing false shape with zero counts. Enabled status returns actual counts. Manual sync test calls POST twice while the fake upstream blocks: first gets 202, second gets 409.

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/api -run 'TestMinerva|TestSourcesEndpoint|TestConfigEndpoint' -v
```

Expected: compile failure/routes absent.

- [ ] **Step 3: Update router/test construction**

`newTestEnv` passes nil by default as the sixth `NewRouter` dependency. Update every `NewRouter` call in tests/main to compile before adding behavior.

- [ ] **Step 4: Implement status and race-free async manual sync**

Nil service status:

```json
{"enabled":false,"ready":false,"syncing":false,"collections":0,"files":0}
```

For POST decode:

```go
var req struct { Full bool `json:"full"` }
```

Call `svc.StartSync(context.Background(), req.Full)` synchronously. Return 202 on nil; return 409 on `errors.Is(err, minerva.ErrSyncInProgress)`; return 500 for other start errors. Because `StartSync` claims the guard before it returns, two simultaneous requests cannot both receive 202.

- [ ] **Step 5: Initialize only when enabled**

In main, if `cfg.Sources.Minerva.Enabled` is false leave service nil and make no Minerva HTTP/SQLite calls. If true, `minerva.Open(cfg.DataDir, cfg.Sources.Minerva)`, defer close after HTTP shutdown, and when `Ready` is false call `StartSync(processContext, false)` without blocking server startup.

- [ ] **Step 6: Add periodic incremental sync**

Start one ticker using `time.Duration(spec.SyncIntervalHours) * time.Hour`, falling back to 24h for non-positive values. Each tick calls `StartSync(processContext, false)`; ignore only `ErrSyncInProgress`, log any other error. Stop ticker on process context cancellation.

- [ ] **Step 7: Surface source state**

Add Minerva to `/api/sources`, `/api/config`, and admin dashboard. Use `not_configured` when disabled, `syncing` when active, `degraded` when enabled with `LastError` or no usable index after a failed sync, and `ok` when ready with no current error. Existing source entries remain.

- [ ] **Step 8: Run API/main tests**

```bash
go test ./internal/api ./cmd/gamarr -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/gamarr/main.go internal/api/api.go internal/api/minerva.go internal/api/main_test.go internal/api/router_test.go internal/api/admin.go
git commit -m "feat: manage Minerva index lifecycle"
```

---

### Task 9: Document API and Operator Configuration

**Files:**
- Modify: `internal/api/openapi.json`
- Modify: `internal/api/router_test.go`
- Modify: `README.md`

- [ ] **Step 1: Add failing OpenAPI assertions**

Extend `TestOpenAPISpec` to assert the serialized spec contains `torrent_file_index`, `/api/minerva/status`, and `/api/minerva/sync`.

- [ ] **Step 2: Verify failure**

```bash
go test ./internal/api -run TestOpenAPISpec -v
```

Expected: FAIL because fields/routes are not yet documented.

- [ ] **Step 3: Add OpenAPI fields/routes**

Document optional fields:

```json
"torrent_file_index": {"type":["integer","null"],"minimum":0},
"torrent_file_path":  {"type":"string"},
"torrent_file_size":  {"type":"integer","format":"int64","minimum":0}
```

Document `GET /api/minerva/status` and admin `POST /api/minerva/sync`, with request `{ "full": boolean }`, 202 accepted and 409 sync-in-progress responses.

- [ ] **Step 4: Add README configuration section**

Document exactly:

```text
MINERVA_ENABLED=false
MINERVA_URL=https://minerva-archive.org/
MINERVA_ASSETS_URL=https://minerva-archive.org/assets/
MINERVA_SYNC_INTERVAL_HOURS=24
index: <DATA_DIR>/minerva/index.db
qBittorrent required for Minerva downloads
initial sync: automatic only when enabled and index is not ready
scheduled sync: incremental
manual sync: POST /api/minerva/sync
full rebuild: POST /api/minerva/sync with {"full":true}
```

State that sync downloads only torrent metadata, not ROM payloads; qBittorrent validates and downloads selected files.

- [ ] **Step 5: Run test**

```bash
go test ./internal/api -run TestOpenAPISpec -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/openapi.json internal/api/router_test.go README.md
git commit -m "docs: document Minerva source support"
```

---

### Task 10: Full Regression and Upstream PR Readiness

**Files:**
- Review all files changed on `feat/minerva-source`.

- [ ] **Step 1: Format and check whitespace**

```bash
gofmt -w internal/minerva/*.go internal/sources/*.go internal/qbit/*.go internal/download/*.go internal/search/*.go internal/api/*.go internal/models/*.go cmd/gamarr/*.go
git diff --check
```

Expected: no whitespace errors.

- [ ] **Step 2: Run full tests**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run race-sensitive tests**

```bash
go test -race ./internal/minerva ./internal/qbit ./internal/download ./internal/api -count=1
```

Expected: PASS.

- [ ] **Step 4: Vet and build**

```bash
go vet ./...
go build ./cmd/gamarr
```

Expected: both exit 0.

- [ ] **Step 5: Verify disabled zero-side-effect behavior**

Add/retain a test with Minerva disabled and a counted fake Minerva HTTP endpoint; router construction, `/api/search`, scheduler construction, and `/api/minerva/status` must leave the count at zero.

```bash
go test ./internal/sources ./internal/config ./internal/api -run 'Minerva|Default' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Review the diff against the spec**

```bash
git diff --stat main...HEAD
git diff main...HEAD -- internal/minerva internal/sources internal/qbit internal/download internal/search internal/api internal/models cmd/gamarr README.md
```

Confirm: disabled default; multi-platform configured slugs; local SQLite; incremental 24h + manual/full sync; no aria2c; qB-only payload path; live file-list validation before start; target-only import; old index survives sync failures; existing sources remain intact.

- [ ] **Step 7: Confirm clean working tree**

After any narrowly scoped verification fix is committed:

```bash
git status --short
```

Expected: no output.

- [ ] **Step 8: Prepare, but do not merge, the upstream PR**

Title:

```text
feat: add optional Minerva Archive source
```

PR body must cover optional/disabled default, metadata-only local index, incremental sync, qB selective download, live-file validation, tests, and qB-only v1 limitation. Target `JeremiahM37/gamarr:main` from `tiagofcp:feat/minerva-source`. Do not merge automatically.
