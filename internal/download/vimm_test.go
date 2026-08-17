package download

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamarr/internal/sources"
)

const vimmGamePageHTML = `<html><body>
<form action="/download" method="POST" id="dl_form" onsubmit="return submitDL(this, 'dialog3')"><input type="hidden" name="mediaId" value="1624"><input type="hidden" name="alt" value="0" disabled><button type="submit">Download</button></form>
<script>function submitDL(theForm,dialogId){theForm.method='GET';return true;}</script>
</body></html>`

func TestParseVimmDownloadForm(t *testing.T) {
	action, media := parseVimmDownloadForm(vimmGamePageHTML)
	if media != "1624" {
		t.Errorf("mediaId = %q, want 1624", media)
	}
	if action != "/download" {
		t.Errorf("action = %q, want /download", action)
	}

	live := `<form action="//dl3.vimm.net/" method="POST" id="dl_form" onsubmit="return submitDL(this, 'dialog3')"><input type="hidden" name="mediaId" value="1624">`
	action, media = parseVimmDownloadForm(live)
	if media != "1624" || action != "//dl3.vimm.net/" {
		t.Errorf("live form action=%q media=%q", action, media)
	}
}

func TestVimmDownloadGETURL(t *testing.T) {
	got := vimmGETURL("https://dl3.vimm.net/", "1624")
	if !strings.Contains(got, "mediaId=1624") {
		t.Errorf("GET URL = %q, want mediaId query", got)
	}
	if strings.HasPrefix(got, "https://dl3.vimm.net/") && !strings.Contains(got, "?") {
		t.Errorf("GET URL = %q, want query string", got)
	}
}

func TestDownloadVimmGame_UsesGETNotPOST(t *testing.T) {
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vault/"):
			_, _ = io.WriteString(w, vimmGamePageHTML)
		case r.URL.Path == "/download":
			methods = append(methods, r.Method+" "+r.URL.RawQuery)
			if r.Method != http.MethodGet {
				http.Error(w, "POST is 400 on current Vimm", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("mediaId") != "1624" {
				http.Error(w, "missing mediaId", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="Super Metroid (Japan, USA).zip"`)
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK\x03\x04fake-zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := newTestConfig(t)
	reg, err := sources.Default()
	if err != nil {
		t.Fatal(err)
	}
	reg.Vimm.BaseURL = srv.URL + "/vault/"
	cfg.Sources = reg

	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadVimmGame("1654", cfg.QBSavePath, jobID)
	if got == "" {
		job, _ := jobs.Get(jobID)
		t.Fatalf("download failed: %+v methods=%v", job, methods)
	}
	if filepath.Base(got) != "Super Metroid (Japan, USA).zip" {
		t.Errorf("filename = %q", filepath.Base(got))
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "PK\x03\x04fake-zip" {
		t.Errorf("file contents = %q", body)
	}
	if len(methods) == 0 {
		t.Fatal("download endpoint was never hit — vault URL is still hardcoded")
	}
	if methods[0] != "GET mediaId=1624" {
		t.Errorf("download requests = %v, want GET with mediaId", methods)
	}
	for _, m := range methods {
		if strings.HasPrefix(m, "POST") {
			t.Errorf("Vimm rejects POST; should not have sent %q", m)
		}
	}
}

func TestDownloadVimmGame_HTMLResponseIsError(t *testing.T) {
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vault/") {
			_, _ = io.WriteString(w, vimmGamePageHTML)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "<html>rate limited</html>")
	}))
	t.Cleanup(srv.Close)

	cfg := newTestConfig(t)
	reg, _ := sources.Default()
	reg.Vimm.BaseURL = srv.URL + "/vault/"
	cfg.Sources = reg
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	if got := m.downloadVimmGame("1654", cfg.QBSavePath, jobID); got != "" {
		t.Errorf("HTML body should not be saved as a ROM, got %q", got)
	}
	job, _ := jobs.Get(jobID)
	errMsg, _ := job["error"].(string)
	if !strings.Contains(strings.ToLower(errMsg), "html") && !strings.Contains(strings.ToLower(errMsg), "web page") {
		t.Errorf("error = %q, want mention of HTML/web page", errMsg)
	}
}
