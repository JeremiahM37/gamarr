package torznab

import (
	"context"
	"crypto/subtle"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gamarr/internal/models"
)

// SearchFunc is the search-and-score function the handler invokes. Returning
// *models.SearchResult so we can re-use the existing slice without copying.
type SearchFunc func(ctx context.Context, query, platformSlug string) []*models.SearchResult

// Handler serves the Torznab API at /torznab/api (and /api alias for
// Prowlarr indexer discovery).
type Handler struct {
	apiKey string
	search SearchFunc
}

// New constructs a Torznab handler. apiKey is checked against ?apikey= when
// non-empty (Prowlarr always sends it). search performs the actual search.
func New(apiKey string, search SearchFunc) *Handler {
	return &Handler{apiKey: apiKey, search: search}
}

// ServeHTTP handles GET /torznab/api.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.apiKey != "" {
		got := r.URL.Query().Get("apikey")
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.apiKey)) != 1 {
			h.writeError(w, "100", "Invalid API Key", http.StatusUnauthorized)
			return
		}
	}

	t := r.URL.Query().Get("t")
	switch t {
	case "", "caps":
		h.handleCaps(w)
	case "search", "console-search", "pc-search", "tvsearch", "movie":
		h.handleSearch(w, r)
	default:
		h.writeError(w, "202", fmt.Sprintf("No such function (%s)", t), http.StatusBadRequest)
	}
}

// ServeHTTPAlias handles the bare /api path Prowlarr probes during indexer
// discovery — Prowlarr expects a Torznab endpoint at /api, not /torznab/api.
// Mounted on exactly /api (not /api/*) so the JSON API tree is unaffected.
func (h *Handler) ServeHTTPAlias(w http.ResponseWriter, r *http.Request) {
	h.ServeHTTP(w, r)
}

func (h *Handler) handleCaps(w http.ResponseWriter) {
	caps := BuildCaps()
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(caps)
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		// Prowlarr's indexer-discovery probe sends t=search with no query.
		// Returning an empty feed is valid Torznab but fails Prowlarr's
		// "Generic Torznab" validator (needs >= 1 well-formed item). Emit a
		// single placeholder item describing the endpoint instead — the
		// "gamarr-placeholder-" GUID prefix tells downstream *arr apps not
		// to grab it.
		slog.Info("torznab empty search (health probe)", "remote", r.RemoteAddr)
		h.writeProbeResponse(w, r)
		return
	}

	platformSlug := r.URL.Query().Get("platform")
	slog.Info("torznab search", "query", query, "platform", platformSlug, "remote", r.RemoteAddr)

	results := h.search(r.Context(), query, platformSlug)

	items := make([]Item, 0, len(results))
	for _, res := range results {
		items = append(items, ResultToItem(res))
	}

	rss := RSS{
		Version: "2.0",
		Xmlns:   "http://torznab.com/schemas/2015/feed",
		Channel: Channel{
			Title:       "Gamarr",
			Description: "Game search results from Gamarr",
			Items:       items,
		},
	}
	h.writeXML(w, rss)
}

func (h *Handler) writeProbeResponse(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	probeURL := fmt.Sprintf("%s://%s/torznab/api?t=caps", scheme, r.Host)

	// Match every field Prowlarr's validator requires: pubDate (RFC-1123),
	// size > 0, enclosure with length+type, torznab:attr category + seeders.
	items := []Item{{
		Title:    "Gamarr Torznab endpoint — pass ?q=<title> to search",
		GUID:     fmt.Sprintf("gamarr-placeholder-%s", probeURL),
		Size:     1024,
		Link:     probeURL,
		Category: "Console",
		PubDate:  time.Now().UTC().Format(time.RFC1123Z),
		Enclosure: &Enclosure{
			URL:    probeURL,
			Length: 1024,
			Type:   "application/x-bittorrent",
		},
		Attrs: []Attr{
			{Name: "category", Value: "1000"},
			{Name: "seeders", Value: "0"},
			{Name: "peers", Value: "0"},
		},
	}}
	rss := RSS{
		Version: "2.0",
		Xmlns:   "http://torznab.com/schemas/2015/feed",
		Channel: Channel{
			Title:       "Gamarr",
			Description: "Game search endpoint",
			Items:       items,
		},
	}
	h.writeXML(w, rss)
}

func (h *Handler) writeXML(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(status)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(&Error{Code: code, Description: description})
}
