# Minerva Source Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional multi-platform Minerva Archive source to Gamarr with an incrementally maintained local SQLite index and qBittorrent selective-file downloads.

**Architecture:** Gamarr will build its own searchable Minerva index from the real Minerva `.torrent` metadata, store that index under `DATA_DIR/minerva/index.db`, and query it locally during searches. Minerva results stay torrent results but carry a target file index/path; qBittorrent is used only for payload download, with Gamarr validating the real torrent file list before enabling the requested file. Sync is incremental: a lightweight assets-listing validator detects no-change runs, and only new/changed collection torrents are downloaded and parsed.

**Tech Stack:** Go 1.24+, `modernc.org/sqlite` already present in Gamarr, `net/http`, `crypto/sha1` for BitTorrent v1 info hashes, existing qBittorrent Web API client, chi HTTP API, existing Gamarr search/download/health pipelines.

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

New focused package:

```text
internal/minerva/
├── bencode.go       # minimal safe bencode decoder + raw info-dictionary boundaries
├── torrent.go       # .torrent -> TorrentMeta/FileMeta + SHA-1 info hash
├── index.go         # SQLite schema, upserts, status metadata, local search
├── client.go        # Minerva assets listing/version and torrent HTTP retrieval
├── sync.go          # incremental/full sync orchestration and concurrency guard
└── *_test.go        # package-local unit/integration tests
```

Existing files changed by responsibility:

```text
internal/sources/sources.go         Minerva registry type
internal/sources/defaults.json      disabled default + endpoints + platform collection paths
internal/sources/load.go            Minerva env overrides
internal/sources/sources_test.go    registry/default/override compatibility
internal/config/config_test.go      disabled-by-default assertion
internal/models/models.go           generic selective-torrent metadata on search/download models
internal/qbit/client.go             paused add, file priorities, start/resume, file progress
internal/qbit/client_test.go        qB API contract tests
internal/download/manager.go        selective Minerva orchestration + target-only import
internal/download/manager_test.go   mismatch, priority, completion/import tests
internal/search/minerva.go          Minerva service -> SearchResult adapter + health
internal/search/minerva_test.go     local search result mapping
internal/api/api.go                 service dependency, /api/search, /api/download, routes
internal/api/requests.go            request search/download routing
internal/api/torznab_wire.go        Minerva in Torznab search fan-out
internal/api/minerva.go             status/sync handlers
internal/api/main_test.go           router test fixture accepts optional Minerva service
internal/api/router_test.go         source/status route tests
internal/api/openapi.json           selective fields + Minerva endpoints
internal/api/admin.go               Minerva source status on admin dashboard
cmd/gamarr/main.go                  service lifecycle, initial/periodic sync, scheduler fan-out
README.md                           configuration and behavior
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
- Produces: `sources.Registry.Minerva sources.MinervaSpec`
- Produces:

```go
type MinervaSpec struct {
    Enabled           bool              `json:"enabled"`
    BaseURL           string            `json:"base_url"`
    AssetsURL         string            `json:"assets_url"`
    SyncIntervalHours int               `json:"sync_interval_hours"`
    PlatformPaths     map[string]string `json:"platform_paths"`
}
```

- Environment overrides: `MINERVA_ENABLED`, `MINERVA_URL`, `MINERVA_ASSETS_URL`, `MINERVA_SYNC_INTERVAL_HOURS`.

- [ ] **Step 1: Write failing registry/default tests**

Add explicit assertions to `internal/sources/sources_test.go`:

```go
func TestDefault_MinervaDisabledAndConfigured(t *testing.T) {
    r, err := Default()
    if err != nil {
        t.Fatal(err)
    }
    if r.Minerva.Enabled {
        t.Fatal("Minerva must be disabled by default")
    }
    if r.Minerva.BaseURL != "https://minerva-archive.org/" {
        t.Fatalf("BaseURL=%q", r.Minerva.BaseURL)
    }
    if r.Minerva.AssetsURL != "https://minerva-archive.org/assets/" {
        t.Fatalf("AssetsURL=%q", r.Minerva.AssetsURL)
    }
    if r.Minerva.SyncIntervalHours != 24 {
        t.Fatalf("SyncIntervalHours=%d", r.Minerva.SyncIntervalHours)
    }
    if r.Minerva.PlatformPaths["nds"] != "No-Intro/Nintendo - Nintendo DS (Decrypted)/" {
        t.Fatalf("nds path=%q", r.Minerva.PlatformPaths["nds"])
    }
}
```

Extend `TestApplyEnvOverrides` with one case that sets all four Minerva overrides and asserts the parsed values.

- [ ] **Step 2: Run the source tests and confirm failure**

Run:

```bash
go test ./internal/sources -run 'TestDefault_Minerva|TestApplyEnvOverrides' -v
```

Expected: compile failure because `Registry.Minerva` and `MinervaSpec` do not exist.

- [ ] **Step 3: Add the source type and disabled embedded defaults**

Add `Minerva MinervaSpec` to `Registry`, define `MinervaSpec`, and add this shape to `defaults.json`:

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

Collections that return 404 during sync are treated as unavailable for that platform, not as a boot failure.

- [ ] **Step 4: Implement env overrides without breaking external registry files**

In `ApplyEnvOverrides`, only override a Minerva value when the corresponding env variable is non-empty. Parse `MINERVA_ENABLED` with accepted values `true/false`, `1/0`, `yes/no`; ignore invalid values and retain the registry value. Parse `MINERVA_SYNC_INTERVAL_HOURS` as a positive integer, otherwise retain the registry value.

External registry JSON that omits `minerva` must still load, yielding a zero-value disabled Minerva block.

- [ ] **Step 5: Add config-level disabled-by-default coverage**

In `internal/config/config_test.go`, add `MINERVA_ENABLED`, `MINERVA_URL`, `MINERVA_ASSETS_URL`, and `MINERVA_SYNC_INTERVAL_HOURS` to the env cleanup list and assert:

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
- Produces:

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

- `InfoHash` is lowercase hexadecimal SHA-1 of the exact raw bencoded `info` dictionary bytes.
- Paths returned by `ParseTorrent` are slash-separated relative paths; absolute paths, `..` components, NUL bytes, and empty filenames are rejected.

- [ ] **Step 1: Write failing single-file and multi-file tests**

Use literal bencoded fixtures in `torrent_test.go`, including:

```go
func TestParseTorrentMultiFile(t *testing.T) {
    data := []byte("d4:infod5:filesld6:lengthi3e4:pathl5:a.ndseed6:lengthi4e4:pathl3:dir5:b.ndseee4:name10:collectionee")
    got, err := ParseTorrent(data)
    if err != nil {
        t.Fatal(err)
    }
    if got.Name != "collection" || len(got.Files) != 2 {
        t.Fatalf("got %+v", got)
    }
    if got.Files[0].Index != 0 || got.Files[0].Path != "a.nds" || got.Files[0].Size != 3 {
        t.Fatalf("first=%+v", got.Files[0])
    }
    if got.Files[1].Index != 1 || got.Files[1].Path != "dir/b.nds" || got.Files[1].Size != 4 {
        t.Fatalf("second=%+v", got.Files[1])
    }
    if len(got.InfoHash) != 40 {
        t.Fatalf("info hash=%q", got.InfoHash)
    }
}
```

Also add a single-file torrent test and table tests rejecting `../escape.nds`, `/absolute.nds`, empty path components, malformed bencode, and duplicate/invalid `info` dictionaries.

- [ ] **Step 2: Run the parser tests and confirm failure**

```bash
go test ./internal/minerva -run TestParseTorrent -v
```

Expected: compile failure because `ParseTorrent` does not exist.

- [ ] **Step 3: Implement a minimal bounded bencode decoder**

`bencode.go` must decode integers, byte strings, lists, and dictionaries with recursion-depth and input-bound checks. While decoding the top-level dictionary, record the byte offsets of the `info` value so `torrent.go` can hash the exact raw bytes instead of re-encoding a map.

Use an internal representation only; do not export a generic bencode API.

- [ ] **Step 4: Implement `ParseTorrent`**

Support BitTorrent v1 single-file (`length`) and multi-file (`files`) structures. Preserve the file order from the torrent because qBittorrent file indices are order-sensitive. Compute:

```go
sum := sha1.Sum(infoRaw)
infoHash := hex.EncodeToString(sum[:])
```

Normalize each path with `path.Clean`, reject paths whose clean value is `.` or begins with `../`, and reject any leading `/`.

- [ ] **Step 5: Run parser tests**

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

### Task 3: Add the Local SQLite Index and Local Search

**Files:**
- Create: `internal/minerva/index.go`
- Create: `internal/minerva/index_test.go`

**Interfaces:**
- Consumes: `TorrentMeta`, `FileMeta` from Task 2.
- Produces:

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
    PlatformSlug string
    BrowsePath   string
    BundleVersion string
    TorrentURL   string
    InfoHash     string
    ETag         string
    LastModified string
    ContentSHA256 string
}

type Index struct { /* owns *sql.DB */ }

func OpenIndex(path string) (*Index, error)
func (i *Index) Close() error
func (i *Index) ReplaceCollection(ctx context.Context, rec CollectionRecord, files []FileMeta) error
func (i *Index) Search(ctx context.Context, query, platformSlug string, limit int) ([]IndexedFile, error)
func (i *Index) Collection(ctx context.Context, platformSlug string) (CollectionRecord, bool, error)
func (i *Index) SetState(ctx context.Context, key, value string) error
func (i *Index) State(ctx context.Context, key string) (string, bool, error)
func (i *Index) Counts(ctx context.Context) (collections, files int, err error)
func (i *Index) Reset(ctx context.Context) error
```

- [ ] **Step 1: Write failing schema/upsert/search tests**

Create an index in `t.TempDir()` and assert a collection replacement is atomic and searchable:

```go
func TestIndexReplaceAndSearch(t *testing.T) {
    idx, err := OpenIndex(filepath.Join(t.TempDir(), "index.db"))
    if err != nil { t.Fatal(err) }
    defer idx.Close()

    rec := CollectionRecord{
        PlatformSlug: "nds", BrowsePath: "No-Intro/Nintendo - Nintendo DS (Decrypted)/",
        BundleVersion: "v0.3", TorrentURL: "https://example.test/nds.torrent",
        InfoHash: strings.Repeat("a", 40), ContentSHA256: "fixture",
    }
    files := []FileMeta{{Index: 7, Path: "Pokemon - HeartGold Version (Europe).nds", Name: "Pokemon - HeartGold Version (Europe).nds", Size: 134217728}}
    if err := idx.ReplaceCollection(context.Background(), rec, files); err != nil { t.Fatal(err) }

    hits, err := idx.Search(context.Background(), "pokemon heartgold", "nds", 20)
    if err != nil { t.Fatal(err) }
    if len(hits) != 1 || hits[0].FileIndex != 7 || hits[0].InfoHash != rec.InfoHash {
        t.Fatalf("hits=%+v", hits)
    }
}
```

Add a replacement test proving old files disappear only after the replacement transaction commits, plus platform-scoping and limit tests.

- [ ] **Step 2: Run index tests and confirm failure**

```bash
go test ./internal/minerva -run TestIndex -v
```

Expected: compile failure because `OpenIndex` and index types do not exist.

- [ ] **Step 3: Implement the schema**

Use the already-present `modernc.org/sqlite` driver and create:

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
CREATE INDEX IF NOT EXISTS idx_minerva_files_platform_name
  ON minerva_files(platform_slug, name);
CREATE TABLE IF NOT EXISTS minerva_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

Enable foreign keys on the connection.

- [ ] **Step 4: Implement transactional replacement and search**

`ReplaceCollection` starts one SQL transaction, upserts the collection row, deletes old file rows for that platform, inserts the new rows, and commits only after every insert succeeds.

`Search` tokenizes the query into lowercase whitespace-separated words and builds parameterized `LOWER(name) LIKE ?` predicates joined with `AND`. Require a non-empty platform slug for v1 to avoid expensive cross-platform scans; return an empty result if the platform has no indexed collection.

- [ ] **Step 5: Run index tests**

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

### Task 4: Implement Incremental Minerva Sync

**Files:**
- Create: `internal/minerva/client.go`
- Create: `internal/minerva/sync.go`
- Create: `internal/minerva/sync_test.go`

**Interfaces:**
- Consumes: `sources.MinervaSpec`, `Index`, `ParseTorrent`.
- Produces:

```go
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

type Service struct { /* spec, HTTP client, index, sync mutex/state */ }

func Open(dataDir string, spec sources.MinervaSpec) (*Service, error)
func (s *Service) Close() error
func (s *Service) Sync(ctx context.Context, force bool) (SyncReport, error)
func (s *Service) Search(ctx context.Context, query, platformSlug string, limit int) ([]IndexedFile, error)
func (s *Service) Status(ctx context.Context) Status
func (s *Service) Ready(ctx context.Context) bool
```

- [ ] **Step 1: Write failing no-change and changed-content sync tests**

Use one `httptest.Server` that serves an assets listing and two fake `.torrent` files. The first sync returns 200 responses and creates rows; the second assets request honors `If-None-Match` and returns 304. Assert the second report updates zero collections and the torrent endpoints receive no second request.

Also test a changed assets validator where one torrent returns 304 and the other returns changed bytes; assert only the changed collection is reparsed/replaced.

- [ ] **Step 2: Run sync tests and confirm failure**

```bash
go test ./internal/minerva -run TestSync -v
```

Expected: compile failure because `Service` does not exist.

- [ ] **Step 3: Implement assets bundle discovery**

`client.go` GETs `spec.AssetsURL` with `User-Agent: Gamarr/1.0`. Parse directory links matching:

```text
Minerva_Myrient_v<major>.<minor>
```

Choose the highest numeric semantic pair. Store the assets-listing `ETag`, `Last-Modified`, and SHA-256 body hash in `minerva_state` keys:

```text
assets_etag
assets_last_modified
assets_body_sha256
bundle_version
```

On normal sync send `If-None-Match` and `If-Modified-Since`. If the server answers 304, return immediately without requesting any `.torrent` files.

If the assets listing is 200 but its SHA-256 and selected bundle version match the stored values, also return immediately unless `force=true`.

- [ ] **Step 4: Implement deterministic torrent URL construction**

For each configured `PlatformPaths[slug]`, remove the trailing slash, replace path separators `/` with ` - `, and construct the filename:

```text
Minerva_Myrient - <collection-name>.torrent
```

Then URL-escape the filename and join it below:

```text
<AssetsURL>/Minerva_Myrient_<bundle-version>/<escaped-filename>
```

This follows Minerva's current public asset naming used by existing Minerva tooling; keep `AssetsURL` and platform paths overrideable through the source registry.

- [ ] **Step 5: Implement per-collection incremental fetch**

For a collection already in SQLite, send its stored `ETag` and `Last-Modified`. Outcomes:

```text
304 -> increment Unchanged; do not parse
404 -> increment Missing; preserve the previous indexed collection if one exists
200 -> read with a 256 MiB metadata body cap, SHA-256 the bytes, compare with stored content_sha256
same SHA-256 -> update validators only, increment Unchanged
different/new -> ParseTorrent, ReplaceCollection, increment Updated
other HTTP/error -> return sync error while leaving the previous collection untouched
```

Do not delete a previously good collection because a transient sync request fails.

- [ ] **Step 6: Add concurrency guard and full rebuild**

Only one `Sync` may run at a time. A second call returns a typed `ErrSyncInProgress`. `force=true` bypasses validators/content equality and reparses every reachable configured collection after resolving the current bundle version. `Index.Reset` is not called before a full sync; replace collections one-by-one so failure does not erase the old index.

After a successful sync, store `last_sync` as RFC3339 UTC. `Status.Ready` is true when the index contains at least one collection.

- [ ] **Step 7: Run sync package tests**

```bash
go test ./internal/minerva -v
```

Expected: PASS, including 304/no-change, changed-only, 404-preserves-old-index, malformed-torrent-preserves-old-index, and concurrent-sync tests.

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
- Extends `TorrentFile`:

```go
Progress float64 `json:"progress"`
```

- Produces:

```go
func (c *Client) AddTorrentPaused(torrentURL, title, savePath, category string) bool
func (c *Client) SetFilePriority(hash string, ids []int, priority int) bool
func (c *Client) StartTorrent(hash string) bool
```

- [ ] **Step 1: Write failing qB API request-shape tests**

Add tests using `httptest.Server` that assert:

```text
POST /api/v2/torrents/add       contains urls, savepath, category and paused/stopped flag
POST /api/v2/torrents/filePrio  contains hash, id="0|1|2", priority="0"
POST /api/v2/torrents/start     contains hashes=<hash>
```

Also assert `StartTorrent` falls back to `/api/v2/torrents/resume` when `/start` returns 404, matching the existing stop/pause compatibility style.

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/qbit -run 'TestAddTorrentPaused|TestSetFilePriority|TestStartTorrent' -v
```

Expected: compile failure because the methods do not exist.

- [ ] **Step 3: Implement paused add without changing `AddTorrent`**

Keep existing `AddTorrent` behavior untouched. Factor the common form submission internally if useful, but preserve every existing qB 5.2 response compatibility test. `AddTorrentPaused` must request a non-starting add; send the modern stopped flag and legacy paused flag in the form so supported qB versions keep payload transfer stopped until priorities are applied.

- [ ] **Step 4: Implement file priority and start/resume**

`SetFilePriority` rejects an empty id slice, joins integer ids with `|`, posts to `/api/v2/torrents/filePrio`, and uses the same 403 re-auth behavior as other mutating methods. Accept any 2xx response.

`StartTorrent` posts to `/api/v2/torrents/start`; on 404 retry `/api/v2/torrents/resume`.

- [ ] **Step 5: Run all qB tests**

```bash
go test ./internal/qbit -v
```

Expected: PASS, including all existing legacy and qBittorrent 5.2 tests.

- [ ] **Step 6: Commit**

```bash
git add internal/qbit/client.go internal/qbit/client_test.go
git commit -m "feat: add qBittorrent selective file controls"
```

---

### Task 6: Add Safe Selective Torrent Download and Target-Only Import

**Files:**
- Modify: `internal/download/manager.go`
- Modify: `internal/download/manager_test.go`

**Interfaces:**
- Consumes qB methods from Task 5.
- Produces:

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

- Minerva selective jobs persist private replay metadata on the job row: `source=minerva`, `torrent_file_index`, `torrent_file_path`, `torrent_file_size`, `download_url`.

- [ ] **Step 1: Write failing mismatch-safety test**

Use a fake qB server where the indexed target says index 7/path `HeartGold.nds`, but qB returns index 7/path `Different.nds`. Assert:

```text
- job reaches error
- filePrio is never called
- start/resume is never called
- no library file is created
```

- [ ] **Step 2: Write failing successful-selection test**

Fake a new torrent with files 0, 1, 2. Assert the call order is:

```text
add paused -> read file list -> priority 0 for all indices -> priority 7 for target -> start
```

When the target file later reports `progress: 1`, assert only that file is imported under `GAMES_ROMS_PATH/<platform_slug>/`.

- [ ] **Step 3: Run focused download tests and confirm failure**

```bash
go test ./internal/download -run 'TestDownloadSelectiveTorrent' -v
```

Expected: compile failure because `DownloadSelectiveTorrent` does not exist.

- [ ] **Step 4: Implement pre-existing torrent safety**

Before adding, query qBittorrent by configured category for `infoHash`.

Behavior:

```text
new hash:
  add paused
  wait for metadata/file list
  validate target
  set all files priority 0
  set target priority 7
  start

existing hash:
  do not reset all priorities
  validate target
  set target priority 7 only
  start if stopped
```

This preserves another active Minerva target or a user's pre-existing torrent instead of zeroing its wanted files.

- [ ] **Step 5: Implement strict target validation**

Poll `GetTorrentFiles(infoHash)` for metadata for at most 30 seconds. Find the entry whose `Index == fileIndex`; normalize both indexed and qB paths to slash-separated relative clean paths. Require exact normalized path equality and, when `fileSize > 0`, exact size equality.

On mismatch, fail before changing file priorities or starting payload transfer. If this invocation added a new torrent, remove only that newly-added torrent/data; never delete a torrent that existed before the request. Record `search.RecordDownloadFail("minerva", reason)`.

- [ ] **Step 6: Implement a target-file watcher**

Poll the qB file list every 5 seconds and watch only `TorrentFile.Progress` for `fileIndex`. Do not use the collection torrent's aggregate `Progress` as completion criteria.

When complete, locate the physical source safely from qB's `Torrent.SavePath` plus the qB-returned relative file name. Verify the final cleaned path remains below `SavePath` before touching it.

- [ ] **Step 7: Reuse existing scan/import primitives for one file**

Run the same ClamAV path used by DDL for the completed target file. Import only the target file to:

```text
<GAMES_ROMS_PATH>/<sanitized platform_slug>/<sanitized basename>
```

Use the existing `importContent` method instead of raw `os.Rename`, so move/hardlink/symlink/copy settings remain consistent with the rest of Gamarr. Track the library source as `minerva`, write the sidecar using source `minerva`, mark the job completed, and call `search.RecordDownloadSuccess("minerva")`.

- [ ] **Step 8: Add retry support for Minerva jobs**

Extend the existing retry dispatch in `manager.go` so a failed Minerva selective job can replay only when all private fields (`download_url`, `info_hash`, file index/path/size, platform fields) are present. Keep normal torrent and DDL retry behavior unchanged.

- [ ] **Step 9: Run download tests**

```bash
go test ./internal/download -v
```

Expected: PASS, including mismatch-before-start, pre-existing-torrent priority preservation, target-only completion/import, and retry tests.

- [ ] **Step 10: Commit**

```bash
git add internal/download/manager.go internal/download/manager_test.go
git commit -m "feat: download selected Minerva torrent files"
```

---

### Task 7: Expose Minerva Results Through Existing Search and Download Models

**Files:**
- Modify: `internal/models/models.go`
- Create: `internal/search/minerva.go`
- Create: `internal/search/minerva_test.go`
- Modify: `internal/api/api.go`
- Modify: `internal/api/requests.go`
- Modify: `internal/api/torznab_wire.go`
- Modify: `cmd/gamarr/main.go`

**Interfaces:**
- Adds to both `models.SearchResult` and `models.DownloadRequest`:

```go
TorrentFileIndex *int   `json:"torrent_file_index,omitempty"`
TorrentFilePath  string `json:"torrent_file_path,omitempty"`
TorrentFileSize  int64  `json:"torrent_file_size,omitempty"`
```

- Produces:

```go
func SearchMinerva(svc *minerva.Service, query, platformSlug string) []*models.SearchResult
```

- [ ] **Step 1: Write failing SearchResult mapping test**

Seed a temporary Minerva index with a HeartGold row, call `SearchMinerva`, and assert:

```go
if got[0].Indexer != "Minerva" || got[0].SourceType != "torrent" || got[0].DownloadProtocol != "torrent" {
    t.Fatalf("result=%+v", got[0])
}
if got[0].TorrentFileIndex == nil || *got[0].TorrentFileIndex != 7 {
    t.Fatalf("file index=%v", got[0].TorrentFileIndex)
}
if got[0].Size != 134217728 || got[0].SafetyScore != 95 {
    t.Fatalf("result=%+v", got[0])
}
```

- [ ] **Step 2: Run the test and confirm failure**

```bash
go test ./internal/search -run TestSearchMinerva -v
```

Expected: compile failure because selective fields and `SearchMinerva` do not exist.

- [ ] **Step 3: Implement the generic selective metadata fields and adapter**

Use a pointer for `TorrentFileIndex` so file index 0 is distinguishable from “not a selective result.” `SearchMinerva` returns nil without network access when `svc == nil`, Minerva is not ready, the platform slug is empty/all, or the Minerva health circuit is open.

Map each local hit to:

```text
Indexer: Minerva
SourceType: torrent
DownloadProtocol: torrent
DownloadURL: collection .torrent URL
InfoHash: parsed info hash
Size: selected file size
SizeHuman: search.HumanSize(size)
SafetyScore: 95
PlatformSlug: indexed platform
TorrentFileIndex/Path/Size: indexed target
```

On a successful local query call `RecordSearchSuccess("minerva")`; on an SQLite error call `RecordSearchFail("minerva", err.Error())`.

- [ ] **Step 4: Route selective downloads before the generic torrent branch**

In both `/api/download` and `/api/requests/{id}/download`, when `TorrentFileIndex != nil`, require `DownloadURL`, `InfoHash`, and `TorrentFilePath`, then call `DownloadSelectiveTorrent`. Do not route Minerva results through `DownloadTorrent`, because that path may fall back to Transmission/Deluge and organizes the whole torrent root.

- [ ] **Step 5: Add Minerva to every search fan-out**

Add one conditional Minerva goroutine to all four search paths:

```text
/api/search                    internal/api/api.go
request search                 internal/api/requests.go
Torznab search                 internal/api/torznab_wire.go
scheduler searchFn             cmd/gamarr/main.go
```

Do not blindly change every `wg.Add(3)` to 4: calculate the base three existing goroutines, then add Minerva only when a non-nil enabled service is wired. This guarantees disabled Minerva performs zero external/index calls.

- [ ] **Step 6: Preserve torrent filtering/scoring semantics**

Keep Minerva `SourceType="torrent"` so it passes through existing `FilterGameResults` and `ScoreResults`. Its `Size` is the selected ROM size, not the collection size. Leave seeders at zero in v1 rather than adding a second live Minerva API dependency to every local search.

- [ ] **Step 7: Run model/search/API compile tests**

```bash
go test ./internal/search ./internal/api ./cmd/gamarr -run 'TestSearchMinerva|TestSearch|TestNonExistent' -count=1
```

Then run:

```bash
go test ./internal/search ./internal/api ./cmd/gamarr -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/models/models.go internal/search/minerva.go internal/search/minerva_test.go internal/api/api.go internal/api/requests.go internal/api/torznab_wire.go cmd/gamarr/main.go
git commit -m "feat: integrate Minerva search results"
```

---

### Task 8: Wire Service Lifecycle, Incremental Schedule, Status, and Manual Sync API

**Files:**
- Modify: `cmd/gamarr/main.go`
- Modify: `internal/api/api.go`
- Create: `internal/api/minerva.go`
- Modify: `internal/api/main_test.go`
- Modify: `internal/api/router_test.go`
- Modify: `internal/api/admin.go`

**Interfaces:**
- `api.Server` gains `minerva *minerva.Service`.
- `NewRouter` becomes:

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

- Routes:

```text
GET  /api/minerva/status
POST /api/minerva/sync      admin only; JSON {"full": false}
```

- [ ] **Step 1: Write failing router/status tests**

Add a disabled case asserting `/api/minerva/status` returns 200 with `enabled:false, ready:false`, and an enabled temp-service case returning counts.

Add a manual sync test whose fake service endpoint is slow enough to prove `POST /api/minerva/sync` returns 202 immediately, and a second simultaneous POST returns 409 while the first sync is running.

- [ ] **Step 2: Run API tests and confirm failure**

```bash
go test ./internal/api -run 'TestMinerva|TestSourcesEndpoint|TestConfigEndpoint' -v
```

Expected: compile failure/new routes missing.

- [ ] **Step 3: Wire the service into router/test fixtures**

Update `newTestEnv` to pass `nil` by default to `NewRouter`; add a helper field on `testEnv` only when a test explicitly constructs a Minerva service. Update every compile-time `NewRouter` call in tests and main.

- [ ] **Step 4: Implement status and asynchronous manual sync**

`GET /api/minerva/status` returns the service status; if service is nil return:

```json
{"enabled":false,"ready":false,"syncing":false,"collections":0,"files":0}
```

`POST /api/minerva/sync` is `requireAdmin`. Decode an optional body:

```go
var req struct { Full bool `json:"full"` }
```

Start `svc.Sync(context.Background(), req.Full)` in a goroutine and return HTTP 202. If `ErrSyncInProgress`, return HTTP 409. Sync completion/failure is visible through `/api/minerva/status` and logs.

- [ ] **Step 5: Initialize only when explicitly enabled**

In `cmd/gamarr/main.go`:

```text
if cfg.Sources.Minerva.Enabled:
    Open(cfg.DataDir, cfg.Sources.Minerva)
    defer Close()
else:
    keep svc nil and make no Minerva HTTP/SQLite calls
```

If enabled and `Ready` is false, start one initial sync after service construction. Do not block HTTP server startup waiting for the first index build.

- [ ] **Step 6: Add the 24-hour incremental loop**

When enabled, start one goroutine with a ticker using `time.Duration(spec.SyncIntervalHours) * time.Hour`, defaulting to 24 when the configured value is <=0. Each tick calls `Sync(ctx, false)`. Stop the ticker via the process shutdown context. A no-change scheduled run must end after the assets-listing 304/body-hash check from Task 4.

- [ ] **Step 7: Surface Minerva in existing source/config/admin views**

`/api/sources` must include `minerva` with `enabled` based on the config/service and health based on existing source health. `/api/config` adds a Minerva section with only non-secret operational fields. Admin dashboard adds Minerva with `not_configured`, `syncing`, `degraded`, or `ok` derived from service/status/health; leave the existing three source entries intact.

- [ ] **Step 8: Run API and main tests**

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

### Task 9: Document the API and Operator Configuration

**Files:**
- Modify: `internal/api/openapi.json`
- Modify: `README.md`
- Modify: `internal/api/router_test.go`

**Interfaces:**
- Documents selective search fields and the two Minerva management endpoints.

- [ ] **Step 1: Add an OpenAPI regression test**

Extend `TestOpenAPISpec` to marshal the decoded document back to bytes and assert it contains:

```text
torrent_file_index
/api/minerva/status
/api/minerva/sync
```

- [ ] **Step 2: Run the test and confirm failure**

```bash
go test ./internal/api -run TestOpenAPISpec -v
```

Expected: FAIL because those schema/path strings are absent.

- [ ] **Step 3: Update OpenAPI SearchResult and download request documentation**

Add optional properties:

```json
"torrent_file_index": {"type":["integer","null"],"minimum":0},
"torrent_file_path":  {"type":"string"},
"torrent_file_size":  {"type":"integer","format":"int64","minimum":0}
```

Add `/api/minerva/status` GET and `/api/minerva/sync` POST with the 202/409 response semantics and `{ "full": boolean }` request body.

- [ ] **Step 4: Document configuration and data flow in README**

Add a concise Minerva section covering exactly:

```text
MINERVA_ENABLED=false                         default
MINERVA_URL=https://minerva-archive.org/
MINERVA_ASSETS_URL=https://minerva-archive.org/assets/
MINERVA_SYNC_INTERVAL_HOURS=24
index path: <DATA_DIR>/minerva/index.db
qBittorrent required for Minerva downloads
initial sync: automatic only when enabled + no usable index
scheduled sync: incremental
manual sync: POST /api/minerva/sync
full rebuild: POST /api/minerva/sync with {"full":true}
```

Explain that Gamarr indexes torrent metadata only; it does not download collection payloads during sync, and each requested ROM is validated/selected by file index/path in qBittorrent.

- [ ] **Step 5: Run docs/API test**

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

### Task 10: Full Regression, Race-Sensitive Review, and Upstream PR Readiness

**Files:**
- Review all files changed on `feat/minerva-source`.
- No unrelated refactors.

**Interfaces:**
- Produces a branch ready for an upstream PR from `tiagofcp:feat/minerva-source` to `JeremiahM37:main`.

- [ ] **Step 1: Run formatting**

```bash
gofmt -w internal/minerva/*.go internal/sources/*.go internal/qbit/*.go internal/download/*.go internal/search/*.go internal/api/*.go internal/models/*.go cmd/gamarr/*.go
```

Review `git diff --check`; expected: no whitespace errors.

- [ ] **Step 2: Run the complete test suite**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the race detector on the new concurrency-sensitive packages**

```bash
go test -race ./internal/minerva ./internal/qbit ./internal/download ./internal/api -count=1
```

Expected: PASS. This specifically checks sync-state locking, concurrent search/sync access, and selective download watchers.

- [ ] **Step 4: Run static checks and build**

```bash
go vet ./...
go build ./cmd/gamarr
```

Expected: both commands exit 0.

- [ ] **Step 5: Verify disabled-by-default zero-side-effect behavior**

Run the config/source tests plus an API test with a Minerva `httptest.Server` counter but `enabled=false`; assert the counter remains zero after `/api/search`, scheduler construction, router construction, and `/api/minerva/status`.

```bash
go test ./internal/sources ./internal/config ./internal/api -run 'Minerva|Default' -count=1 -v
```

Expected: PASS and zero external Minerva calls in the disabled test.

- [ ] **Step 6: Review the branch diff against the approved spec**

```bash
git diff --stat main...HEAD
git diff main...HEAD -- internal/minerva internal/sources internal/qbit internal/download internal/search internal/api internal/models cmd/gamarr README.md
```

Confirm all of these are true before opening the PR:

```text
Minerva disabled by default
multi-platform via configured Gamarr slugs
local SQLite index
incremental 24h sync + manual/full sync
no aria2c dependency
qBittorrent-only payload path
real qB file-list validation before start
target-only import
failed sync preserves old index
Myrient/Vimm/Prowlarr unchanged
```

- [ ] **Step 7: Commit any verification-only fixes, then confirm clean tree**

If verification required code changes, commit them with a narrowly scoped message. Finish with:

```bash
git status --short
```

Expected: no output.

- [ ] **Step 8: Prepare upstream PR body without merging it**

Use a PR title such as:

```text
feat: add optional Minerva Archive source
```

PR body must summarize: optional/disabled default, local metadata-only index, incremental sync, qBittorrent selective-file download, safety validation, test coverage, and explicit v1 limitation to qBittorrent. Do not merge the upstream PR automatically.
