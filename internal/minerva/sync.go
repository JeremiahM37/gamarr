package minerva

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gamarr/internal/sources"
)

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
	closed    bool
	done      chan struct{}
	syncCtx   context.Context
	cancel    context.CancelFunc
}

func Open(dataDir string, spec sources.MinervaSpec) (*Service, error) {
	dir := filepath.Join(dataDir, "minerva")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	index, err := OpenIndex(filepath.Join(dir, "index.db"))
	if err != nil {
		return nil, err
	}
	spec.PlatformPaths = maps.Clone(spec.PlatformPaths)
	return &Service{spec: spec, client: &http.Client{Timeout: 5 * time.Minute}, index: index}, nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	var done chan struct{}
	if s.syncing {
		s.cancel()
		done = s.done
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.client.CloseIdleConnections()
	return s.index.Close()
}

func (s *Service) Search(ctx context.Context, query, platformSlug string, limit int) ([]IndexedFile, error) {
	return s.index.Search(ctx, query, platformSlug, limit)
}

func (s *Service) Ready(ctx context.Context) bool { return s.Status(ctx).Ready }

func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	status := Status{Enabled: s.spec.Enabled, Syncing: s.syncing, LastError: s.lastError}
	s.mu.Unlock()
	collections, files, err := s.index.Counts(ctx)
	if err == nil {
		status.Collections, status.Files = collections, files
		status.Ready = status.Enabled && files > 0
	}
	if err != nil && status.LastError == "" {
		status.LastError = err.Error()
	}
	lastSync, found, err := s.index.State(ctx, "last_sync")
	if err == nil && found {
		status.LastSync, err = time.Parse(time.RFC3339Nano, lastSync)
	}
	if err != nil && status.LastError == "" {
		status.LastError = err.Error()
	}
	return status
}

func (s *Service) beginSync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("minerva service is closed")
	}
	if s.syncing {
		return ErrSyncInProgress
	}
	s.syncing = true
	s.done = make(chan struct{})
	s.syncCtx, s.cancel = context.WithCancel(context.Background())
	return nil
}

func (s *Service) finishSync(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel()
	s.syncing = false
	s.lastError = ""
	if err != nil {
		s.lastError = err.Error()
	}
	close(s.done)
}

func (s *Service) Sync(ctx context.Context, force bool) (report SyncReport, err error) {
	if err = s.beginSync(); err != nil {
		return report, err
	}
	defer func() { s.finishSync(err) }()
	return s.runSync(ctx, force)
}

// StartSync claims the same guard as Sync before returning to the caller.
func (s *Service) StartSync(ctx context.Context, force bool) error {
	if err := s.beginSync(); err != nil {
		return err
	}
	go func() {
		_, err := s.runSync(ctx, force)
		s.finishSync(err)
	}()
	return nil
}

func (s *Service) runSync(ctx context.Context, force bool) (SyncReport, error) {
	s.mu.Lock()
	syncCtx := s.syncCtx
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(syncCtx, cancel)
	defer func() { stop(); cancel() }()
	var report SyncReport
	state := make(map[string]string)
	for _, key := range []string{"assets_etag", "assets_last_modified", "assets_body_sha256", "bundle_version"} {
		value, _, err := s.index.State(ctx, key)
		if err != nil {
			return report, err
		}
		state[key] = value
	}
	etag, lastModified := state["assets_etag"], state["assets_last_modified"]
	if force {
		etag, lastModified = "", ""
	}
	resp, err := s.get(ctx, s.spec.AssetsURL, etag, lastModified)
	if err != nil {
		return report, err
	}
	if resp.StatusCode == http.StatusNotModified && !force {
		resp.Body.Close()
		return report, s.index.SetState(ctx, "last_sync", time.Now().UTC().Format(time.RFC3339Nano))
	}
	body, err := readResponse(resp)
	if err != nil {
		return report, err
	}
	version, err := discoverBundle(body)
	if err != nil {
		return report, err
	}
	if !force && bodyHash(body) == state["assets_body_sha256"] && version == state["bundle_version"] {
		return report, s.saveAssetsState(ctx, resp, body, version)
	}
	slugs := make([]string, 0, len(s.spec.PlatformPaths))
	for slug := range s.spec.PlatformPaths {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		endpoint, err := collectionURL(s.spec.AssetsURL, version, s.spec.PlatformPaths[slug])
		if err != nil {
			return report, err
		}
		report.Checked++
		old, found, err := s.index.Collection(ctx, slug)
		if err != nil {
			return report, err
		}
		etag, lastModified := "", ""
		if found && old.TorrentURL == endpoint && !force {
			etag, lastModified = old.ETag, old.LastModified
		}
		torrentResp, err := s.get(ctx, endpoint, etag, lastModified)
		if err != nil {
			return report, err
		}
		if torrentResp.StatusCode == http.StatusNotFound {
			torrentResp.Body.Close()
			report.Missing++
			continue
		}
		if torrentResp.StatusCode == http.StatusNotModified && found && old.TorrentURL == endpoint && !force {
			torrentResp.Body.Close()
			report.Unchanged++
			continue
		}
		torrentBody, err := readResponse(torrentResp)
		if err != nil {
			return report, err
		}
		if found && old.TorrentURL == endpoint && !force && old.ContentSHA256 == bodyHash(torrentBody) {
			if err := s.index.UpdateValidators(ctx, slug, torrentResp.Header.Get("ETag"), torrentResp.Header.Get("Last-Modified")); err != nil {
				return report, err
			}
			report.Unchanged++
			continue
		}
		meta, err := ParseTorrent(torrentBody)
		if err != nil {
			return report, err
		}
		rec := CollectionRecord{PlatformSlug: slug, BrowsePath: s.spec.PlatformPaths[slug], BundleVersion: version, TorrentURL: endpoint, InfoHash: meta.InfoHash, ETag: torrentResp.Header.Get("ETag"), LastModified: torrentResp.Header.Get("Last-Modified"), ContentSHA256: bodyHash(torrentBody)}
		if err := s.index.ReplaceCollection(ctx, rec, meta.Files); err != nil {
			return report, err
		}
		report.Updated++
		report.Files += len(meta.Files)
	}
	return report, s.saveAssetsState(ctx, resp, body, version)
}

func (s *Service) saveAssetsState(ctx context.Context, resp *http.Response, body []byte, version string) error {
	for _, state := range [][2]string{{"assets_body_sha256", bodyHash(body)}, {"bundle_version", version}, {"assets_etag", resp.Header.Get("ETag")}, {"assets_last_modified", resp.Header.Get("Last-Modified")}, {"last_sync", time.Now().UTC().Format(time.RFC3339Nano)}} {
		if err := s.index.SetState(ctx, state[0], state[1]); err != nil {
			return err
		}
	}
	return nil
}
