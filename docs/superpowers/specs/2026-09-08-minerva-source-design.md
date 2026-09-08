# Minerva Source Design

## Goal

Add Minerva Archive as an optional, multi-platform Gamarr source without replacing the existing Myrient/Vimm/Prowlarr sources. Minerva is disabled by default. The first implementation supports selective downloads through qBittorrent only.

## User flow

1. User enables Minerva in Gamarr.
2. Gamarr maintains a local Minerva search index in SQLite under the configured data directory.
3. Searches run against the local index and return individual ROM files, not whole collection torrents.
4. When a Minerva result is downloaded, Gamarr adds the corresponding torrent to qBittorrent in a non-downloading state, reads the real torrent file list, validates the indexed target, disables all files, enables only the requested file, and starts the torrent.
5. The existing Gamarr download watcher, safety scan, organizer, and library flow continue after the selected file completes.

## Scope

### Included

- Optional Minerva source, disabled by default.
- Multi-platform support for the intersection of platforms already supported by Gamarr and collections available from Minerva.
- Local SQLite index built from Minerva torrent metadata.
- Incremental index synchronization.
- Manual index sync endpoint/action plus automatic sync every 24 hours when Minerva is enabled.
- Initial sync when Minerva is enabled and no usable local index exists.
- qBittorrent selective-file download support.
- Validation of the local index against qBittorrent's actual torrent file list before download.
- Source health/status integration and tests.

### Excluded from v1

- Transmission, Deluge, SABnzbd, NZBGet or aria2c as Minerva download clients.
- Replacing or removing Myrient.
- Adding new Gamarr platforms solely for Minerva.
- Bundling a large static Minerva database in the Git repository.
- Relying on an external third-party file index as the primary source of truth.

## Architecture

### Source configuration

Extend the source registry with a Minerva specification while preserving existing registry compatibility. Minerva remains disabled unless explicitly enabled in configuration.

Conceptually:

```json
{
  "minerva": {
    "enabled": false,
    "base_url": "...",
    "sync_interval_hours": 24
  }
}
```

Exact endpoint defaults will be based on the current public Minerva interfaces and kept overrideable where practical.

### Local index

Use the SQLite dependency already present in Gamarr. Store the Minerva index in the Gamarr data/config area rather than in the repository.

Logical schema:

```text
minerva_torrents
- id / stable remote key
- name
- info_hash
- torrent_url or remote locator
- remote_modified marker when available
- indexed_at

minerva_files
- torrent_id
- file_index
- platform
- path
- name
- size
```

Indexes should support platform + filename/title search and torrent lookup efficiently.

### Incremental synchronization

A sync does not rebuild every torrent on every run.

1. Fetch the lightweight remote torrent/catalog listing needed to discover current Minerva torrents.
2. If the remote endpoint supports `ETag` or `Last-Modified`, send conditional requests and stop immediately on `304 Not Modified`.
3. Compare the remote torrent identity/hash/modified marker with `minerva_torrents`.
4. Download and parse torrent metadata only for new or changed torrents.
5. Leave unchanged torrents and their file rows untouched.
6. Remove or mark stale torrents no longer present upstream only after a successful complete remote listing.
7. Apply index changes transactionally so a failed sync keeps the previous usable index.

A manual full rebuild remains available for recovery/debugging, but it is not the normal scheduled path.

This means the 24-hour job may still perform a small metadata/list request to learn whether anything changed, but it does not repeatedly download and parse every `.torrent`.

### Torrent metadata parsing

Parse `.torrent` metadata natively in Go rather than adding aria2c solely for indexing. The parser only builds the searchable file catalog; it does not download ROM contents.

For each torrent, capture the canonical file order/index, path, name and size required to later validate the target in qBittorrent.

### Search

Minerva search uses the local SQLite index. Results are individual files and include enough Minerva-specific metadata for the download stage, including:

- torrent identity / info hash
- target file index
- target file path/name
- platform
- size
- source type identifying Minerva selective torrent semantics

Existing search scoring/filtering should be reused where it makes sense without pretending a Minerva file is a normal single-file torrent result.

### qBittorrent selective download

Extend the existing qBittorrent client with the minimum capabilities needed for safe selective downloads, including file priority control.

Download flow:

1. Add the collection torrent paused/stopped or otherwise prevent payload download until selection is applied.
2. Resolve the torrent hash in qBittorrent.
3. Read the actual file list with the existing file-list API.
4. Validate the indexed target using file index plus path/name (and size when available).
5. If validation fails, abort the job and request/flag an index refresh rather than downloading an arbitrary file.
6. Set all torrent files to priority 0.
7. Set only the requested target file to the chosen normal/high download priority.
8. Start/resume the torrent.
9. Reuse the existing job watcher and organizer once the selected file completes.

The qBittorrent client remains the only Minerva payload downloader in v1.

## Failure handling

- No local index: Minerva search reports unavailable/degraded and triggers or requests sync; other sources continue working.
- Remote catalog unavailable: preserve the previous index and report degraded health.
- One torrent metadata fetch fails: do not corrupt existing rows; record the failed item and continue where safe.
- Index mismatch at download time: abort selective download before payload starts and mark the Minerva result/index stale.
- qBittorrent unavailable or unsupported: Minerva download is unavailable, while normal Gamarr sources remain unaffected.
- Interrupted sync: transaction/temporary-state strategy leaves the last good index usable.

## Likely code areas

Expected additions/changes include:

```text
internal/minerva/            new package for catalog, torrent parsing, index and sync
internal/sources/            Minerva registry/config support
internal/search/             Minerva search integration / fan-out
internal/qbit/client.go      file priority and selective-download primitives
internal/download/           Minerva download orchestration
internal/api/                manual sync/status endpoints where consistent with existing API
cmd/gamarr/main.go           construction, scheduler wiring and source registration
README.md / docs             configuration and operational documentation
*_test.go                    unit/integration tests
```

The implementation should follow the repository's existing patterns and avoid unrelated refactors.

## Testing

Tests should cover at minimum:

- Minerva disabled by default and produces no external calls.
- Torrent metadata parsing for single-file and multi-file torrents.
- Incremental sync: unchanged, new, changed and removed torrent cases.
- Conditional/no-change sync behavior where HTTP validators are available.
- Transactional preservation of the previous index on failure.
- Platform-scoped local search.
- qBittorrent file priority API calls.
- Successful target validation and selective priority assignment.
- Mismatch protection: no payload starts when index metadata disagrees with qBittorrent.
- Existing Myrient/Vimm/Prowlarr behavior remains unchanged.

## Rollout

Develop on `feat/minerva-source` in `tiagofcp/gamarr`, keep the fork synchronized with `JeremiahM37/gamarr`, validate locally, then open an upstream PR. The PR should present Minerva as an optional source and keep backward compatibility as a primary acceptance constraint.
