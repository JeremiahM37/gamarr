package minerva

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

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

func OpenIndex(dbPath string) (*Index, error) {
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
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
);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

func sqliteDSN(dbPath string) string {
	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}
	pragma := url.Values{"_pragma": {"foreign_keys(ON)"}}
	return dbPath + separator + pragma.Encode()
}

func (i *Index) Close() error {
	return i.db.Close()
}

func (i *Index) ReplaceCollection(ctx context.Context, rec CollectionRecord, files []FileMeta) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO minerva_collections (
  platform_slug, browse_path, bundle_version, torrent_url, info_hash, etag, last_modified, content_sha256, indexed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(platform_slug) DO UPDATE SET
  browse_path = excluded.browse_path,
  bundle_version = excluded.bundle_version,
  torrent_url = excluded.torrent_url,
  info_hash = excluded.info_hash,
  etag = excluded.etag,
  last_modified = excluded.last_modified,
  content_sha256 = excluded.content_sha256,
  indexed_at = excluded.indexed_at`,
		rec.PlatformSlug, rec.BrowsePath, rec.BundleVersion, rec.TorrentURL, rec.InfoHash,
		rec.ETag, rec.LastModified, rec.ContentSHA256); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM minerva_files WHERE platform_slug = ?`, rec.PlatformSlug); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO minerva_files (platform_slug, file_index, path, name, size)
VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, file := range files {
		if _, err := stmt.ExecContext(ctx, rec.PlatformSlug, file.Index, file.Path, file.Name, file.Size); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (i *Index) UpdateValidators(ctx context.Context, platformSlug, etag, lastModified string) error {
	_, err := i.db.ExecContext(ctx, `
UPDATE minerva_collections SET etag = ?, last_modified = ? WHERE platform_slug = ?`,
		etag, lastModified, platformSlug)
	return err
}

func (i *Index) Search(ctx context.Context, query, platformSlug string, limit int) ([]IndexedFile, error) {
	if platformSlug == "" {
		return nil, errors.New("minerva: platform slug is required")
	}
	if limit < 1 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	var statement strings.Builder
	statement.WriteString(`
SELECT f.platform_slug, f.name, f.path, f.size, f.file_index, c.torrent_url, c.info_hash
FROM minerva_files AS f
JOIN minerva_collections AS c ON c.platform_slug = f.platform_slug
WHERE f.platform_slug = ?`)
	args := []any{platformSlug}
	for _, token := range strings.Fields(strings.ToLower(query)) {
		statement.WriteString(` AND LOWER(f.name) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLikeToken(token)+"%")
	}
	statement.WriteString(` ORDER BY f.name, f.file_index LIMIT ?`)
	args = append(args, limit)

	rows, err := i.db.QueryContext(ctx, statement.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []IndexedFile
	for rows.Next() {
		var hit IndexedFile
		if err := rows.Scan(&hit.PlatformSlug, &hit.Name, &hit.Path, &hit.Size, &hit.FileIndex, &hit.TorrentURL, &hit.InfoHash); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func escapeLikeToken(token string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(token)
}

func (i *Index) Collection(ctx context.Context, platformSlug string) (CollectionRecord, bool, error) {
	row := i.db.QueryRowContext(ctx, `
SELECT platform_slug, browse_path, bundle_version, torrent_url, info_hash, etag, last_modified, content_sha256
FROM minerva_collections WHERE platform_slug = ?`, platformSlug)
	var rec CollectionRecord
	if err := row.Scan(&rec.PlatformSlug, &rec.BrowsePath, &rec.BundleVersion, &rec.TorrentURL, &rec.InfoHash,
		&rec.ETag, &rec.LastModified, &rec.ContentSHA256); err != nil {
		if err == sql.ErrNoRows {
			return CollectionRecord{}, false, nil
		}
		return CollectionRecord{}, false, err
	}
	return rec, true, nil
}

func (i *Index) SetState(ctx context.Context, key, value string) error {
	_, err := i.db.ExecContext(ctx, `
INSERT INTO minerva_state (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (i *Index) State(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := i.db.QueryRowContext(ctx, `SELECT value FROM minerva_state WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (i *Index) Counts(ctx context.Context) (collections, files int, err error) {
	if err = i.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM minerva_collections`).Scan(&collections); err != nil {
		return 0, 0, err
	}
	if err = i.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM minerva_files`).Scan(&files); err != nil {
		return 0, 0, err
	}
	return collections, files, nil
}
