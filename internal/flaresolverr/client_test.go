package flaresolverr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "disabled", in: "", want: ""},
		{name: "trim", in: "  http://flaresolverr:8191/  ", want: "http://flaresolverr:8191"},
		{name: "https path", in: "https://solver.example/base/", want: "https://solver.example/base"},
		{name: "uppercase scheme", in: "HTTP://solver.example/", want: "http://solver.example"},
		{name: "relative", in: "flaresolverr:8191", wantErr: true},
		{name: "wrong scheme", in: "ftp://solver.example", wantErr: true},
		{name: "query", in: "http://solver.example/?token=x", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAPIURL(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeAPIURL(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("NormalizeAPIURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	var gotPath string
	var got request
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Errorf("decode raw request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","message":"Challenge solved!","solution":{"status":200,"response":"<script>let allMedia=[{\"ID\":3811}]</script>","userAgent":"solver-agent"}}`))
	}))
	t.Cleanup(srv.Close)

	solution, err := Fetch(context.Background(), srv.URL+"/", "https://vimm.net/vault/4970", 25_000, 74)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1" {
		t.Errorf("request path = %q, want /v1", gotPath)
	}
	if got.Command != "request.get" || got.URL != "https://vimm.net/vault/4970" {
		t.Errorf("request = %+v", got)
	}
	if got.MaxTimeout != 25_000 || got.WaitInSeconds != 5 {
		t.Errorf("timeouts = max %d wait %d", got.MaxTimeout, got.WaitInSeconds)
	}
	if got.TabsTillVerify != 74 {
		t.Errorf("tabs_till_verify = %d, want 74", got.TabsTillVerify)
	}
	if _, ok := raw["tabs_till_verify"]; !ok {
		t.Error("request omitted exact tabs_till_verify key")
	}
	if _, ok := raw["tabsTillVerify"]; ok {
		t.Error("request used unsupported tabsTillVerify key")
	}
	if !strings.Contains(solution.Response, `"ID":3811`) {
		t.Errorf("solution = %+v", solution)
	}
}

func TestRequestTimeoutIncludesSolverOverheadGrace(t *testing.T) {
	want := 25_000*time.Millisecond + 15*time.Second
	if got := requestTimeout(25_000); got != want {
		t.Errorf("requestTimeout = %v, want %v", got, want)
	}
}

func TestValidateMaxTimeout(t *testing.T) {
	// FlareSolverr 3.5.0's first 74-tab attempt has about 15.4 seconds of
	// fixed pauses. Gamarr then requests another five seconds before capture.
	const knownVimmFlowFloor = 20_400
	if MinMaxTimeout <= knownVimmFlowFloor {
		t.Fatalf("minimum timeout %d must exceed the known Vimm flow floor %d", MinMaxTimeout, knownVimmFlowFloor)
	}
	for _, tc := range []struct {
		value   int
		wantErr bool
	}{
		{value: MinMaxTimeout - 1, wantErr: true},
		{value: MinMaxTimeout},
		{value: MaxMaxTimeout},
		{value: MaxMaxTimeout + 1, wantErr: true},
	} {
		if err := ValidateMaxTimeout(tc.value); (err != nil) != tc.wantErr {
			t.Errorf("ValidateMaxTimeout(%d) error = %v, wantErr %v", tc.value, err, tc.wantErr)
		}
	}
}

func TestValidateTabsTillVerify(t *testing.T) {
	for _, tc := range []struct {
		value   int
		wantErr bool
	}{
		{value: -1, wantErr: true},
		{value: 0, wantErr: true},
		{value: 1},
		{value: DefaultVimmTabsTillVerify},
		{value: MaxTabsTillVerify},
		{value: MaxTabsTillVerify + 1, wantErr: true},
	} {
		if err := ValidateTabsTillVerify(tc.value); (err != nil) != tc.wantErr {
			t.Errorf("ValidateTabsTillVerify(%d) error = %v, wantErr %v", tc.value, err, tc.wantErr)
		}
	}
}

func TestCheck(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"msg":"FlareSolverr is ready!","version":"3.5.0"}`))
	}))
	t.Cleanup(srv.Close)

	info, err := Check(context.Background(), srv.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/" {
		t.Errorf("health path = %q, want /", gotPath)
	}
	if info.Version != "3.5.0" {
		t.Errorf("version = %q, want 3.5.0", info.Version)
	}
}

func TestCheckRejectsWrongServiceAndHTTPError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "wrong service", status: http.StatusOK, body: `{"msg":"another service"}`},
		{name: "HTTP error", status: http.StatusServiceUnavailable, body: `unavailable`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			if _, err := Check(context.Background(), srv.URL); err == nil {
				t.Fatal("Check succeeded for an invalid health response")
			}
		})
	}
}

func TestFetchDoesNotDuplicateV1(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"ok","solution":{"response":"<html>ok</html>"}}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := Fetch(context.Background(), srv.URL+"/v1", "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1" {
		t.Errorf("request path = %q, want /v1", gotPath)
	}
}

func TestFetchReturnsStructuredError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","message":"Error solving the challenge. Timeout after 55000 ms."}`))
	}))
	t.Cleanup(srv.Close)
	_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
	if err == nil || !strings.Contains(err.Error(), "Timeout after 55000 ms") {
		t.Fatalf("error = %v, want structured solver message", err)
	}
}

func TestFetchRejectsMalformedAndEmptyResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{name: "malformed", body: `{`, want: "decode FlareSolverr response"},
		{name: "empty page", body: `{"status":"ok","solution":{"response":""}}`, want: "empty page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
