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
	var calls []request
	var rawCalls []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		var got request
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Errorf("decode raw request: %v", err)
		}
		calls = append(calls, got)
		rawCalls = append(rawCalls, raw)
		w.Header().Set("Content-Type", "application/json")
		switch got.Command {
		case "sessions.create":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
			})
		case "request.get":
			_, _ = w.Write([]byte(`{"status":"ok","message":"Challenge solved!","solution":{"status":200,"response":"<script>let allMedia=[{\"ID\":3811}]</script>","userAgent":"solver-agent"}}`))
		case "sessions.destroy":
			_, _ = w.Write([]byte(`{"status":"ok","message":"The session has been removed."}`))
		default:
			t.Errorf("unexpected command %q", got.Command)
		}
	}))
	t.Cleanup(srv.Close)

	solution, err := Fetch(context.Background(), srv.URL+"/", "https://vimm.net/vault/4970", 55_000, 74)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1" {
		t.Errorf("request path = %q, want /v1", gotPath)
	}
	if len(calls) != 3 {
		t.Fatalf("commands = %v, want create, get, destroy", commandNames(calls))
	}
	if calls[0].Command != "sessions.create" || calls[0].Session == "" {
		t.Errorf("create request = %+v", calls[0])
	}
	got := calls[1]
	if got.Command != "request.get" || got.URL != "https://vimm.net/vault/4970" {
		t.Errorf("page request = %+v", got)
	}
	if got.Session != calls[0].Session || calls[2].Session != calls[0].Session {
		t.Errorf("session lifecycle = %+v", calls)
	}
	if got.MaxTimeout != 55_000 || got.WaitInSeconds != 5 {
		t.Errorf("timeouts = max %d wait %d", got.MaxTimeout, got.WaitInSeconds)
	}
	if got.TabsTillVerify == nil || *got.TabsTillVerify != 74 {
		t.Errorf("tabs_till_verify = %v, want 74", got.TabsTillVerify)
	}
	if _, ok := rawCalls[1]["tabs_till_verify"]; !ok {
		t.Error("request omitted exact tabs_till_verify key")
	}
	if _, ok := rawCalls[1]["tabsTillVerify"]; ok {
		t.Error("request used unsupported tabsTillVerify key")
	}
	if calls[2].Command != "sessions.destroy" {
		t.Errorf("last command = %q, want sessions.destroy", calls[2].Command)
	}
	for _, index := range []int{0, 2} {
		for _, key := range []string{"url", "maxTimeout", "waitInSeconds", "tabs_till_verify"} {
			if _, ok := rawCalls[index][key]; ok {
				t.Errorf("%s request unexpectedly included %q", calls[index].Command, key)
			}
		}
	}
	if !strings.Contains(solution.Response, `"ID":3811`) {
		t.Errorf("solution = %+v", solution)
	}
}

func TestFetchRetriesStaleElementInSameSession(t *testing.T) {
	var calls []request
	var rawCalls []map[string]json.RawMessage
	getCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got request
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decode raw request: %v", err)
		}
		calls = append(calls, got)
		rawCalls = append(rawCalls, raw)
		w.Header().Set("Content-Type", "application/json")
		switch got.Command {
		case "sessions.create":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
			})
		case "request.get":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":"error","message":"Error: Error solving the challenge. Message: stale element reference: stale element not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","solution":{"status":200,"response":"<form id=\"dl_form\"><input name=\"mediaId\" value=\"3328\"></form>"}}`))
		case "sessions.destroy":
			_, _ = w.Write([]byte(`{"status":"ok","message":"The session has been removed."}`))
		}
	}))
	t.Cleanup(srv.Close)

	solution, err := Fetch(context.Background(), srv.URL, "https://vimm.net/vault/3453", 55_000, 74)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("commands = %v, want create, get, get, destroy", commandNames(calls))
	}
	wantCommands := []string{"sessions.create", "request.get", "request.get", "sessions.destroy"}
	for i, want := range wantCommands {
		if calls[i].Command != want {
			t.Errorf("call %d command = %q, want %q", i, calls[i].Command, want)
		}
		if calls[i].Session != calls[0].Session {
			t.Errorf("call %d session = %q, want %q", i, calls[i].Session, calls[0].Session)
		}
	}
	first, followup := calls[1], calls[2]
	if first.URL != followup.URL || followup.URL != "https://vimm.net/vault/3453" {
		t.Errorf("request URLs = %q then %q", first.URL, followup.URL)
	}
	if first.WaitInSeconds != 5 || followup.WaitInSeconds != 2 {
		t.Errorf("request waits = %d then %d, want 5 then 2", first.WaitInSeconds, followup.WaitInSeconds)
	}
	if first.MaxTimeout != 55_000 || followup.MaxTimeout != 55_000 {
		t.Errorf("request max timeouts = %d then %d", first.MaxTimeout, followup.MaxTimeout)
	}
	if first.TabsTillVerify == nil || *first.TabsTillVerify != 74 {
		t.Errorf("first tabs_till_verify = %v, want 74", first.TabsTillVerify)
	}
	if _, ok := rawCalls[2]["tabs_till_verify"]; ok || followup.TabsTillVerify != nil {
		t.Error("follow-up request must omit tabs_till_verify")
	}
	if !strings.Contains(solution.Response, `value="3328"`) {
		t.Errorf("solution = %+v", solution)
	}
}

func commandNames(calls []request) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Command)
	}
	return names
}

func TestRequestTimeoutIncludesSolverOverheadGrace(t *testing.T) {
	want := 55_000*time.Millisecond + 15*time.Second
	if got := requestTimeout(55_000); got != want {
		t.Errorf("requestTimeout = %v, want %v", got, want)
	}
}

func TestValidateMaxTimeout(t *testing.T) {
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
		var got request
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch got.Command {
		case "sessions.create":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
			})
		case "request.get":
			_, _ = w.Write([]byte(`{"status":"ok","solution":{"response":"<html>ok</html>"}}`))
		case "sessions.destroy":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
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
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		_ = json.NewDecoder(r.Body).Decode(&got)
		commands = append(commands, got.Command)
		if got.Command == "sessions.create" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
			})
			return
		}
		if got.Command == "sessions.destroy" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","message":"Error solving the challenge. Timeout after 55000 ms."}`))
	}))
	t.Cleanup(srv.Close)
	_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
	if err == nil || !strings.Contains(err.Error(), "Timeout after 55000 ms") {
		t.Fatalf("error = %v, want structured solver message", err)
	}
	if strings.Join(commands, ",") != "sessions.create,request.get,sessions.destroy" {
		t.Errorf("commands = %v, non-stale errors must not retry", commands)
	}
}

func TestFetchRetriesStaleElementOnlyOnce(t *testing.T) {
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		_ = json.NewDecoder(r.Body).Decode(&got)
		commands = append(commands, got.Command)
		w.Header().Set("Content-Type", "application/json")
		switch got.Command {
		case "sessions.create":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
			})
		case "request.get":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"error","message":"StaleElementReferenceException: stale element not found"}`))
		case "sessions.destroy":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
	if err == nil || !strings.Contains(err.Error(), "StaleElementReferenceException") {
		t.Fatalf("error = %v, want the second stale-element error", err)
	}
	if strings.Join(commands, ",") != "sessions.create,request.get,request.get,sessions.destroy" {
		t.Errorf("commands = %v, stale-element response must be retried exactly once", commands)
	}
}

func TestFetchCleanupSurvivesCanceledCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	destroyed := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch got.Command {
		case "sessions.create":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
			})
		case "request.get":
			cancel()
			_, _ = w.Write([]byte(`{"status":"ok","solution":{"response":"<html>ok</html>"}}`))
		case "sessions.destroy":
			destroyed <- struct{}{}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	t.Cleanup(srv.Close)

	_, _ = Fetch(ctx, srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
	select {
	case <-destroyed:
	case <-time.After(time.Second):
		t.Fatal("session cleanup reused the canceled caller context")
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
				var got request
				_ = json.NewDecoder(r.Body).Decode(&got)
				switch got.Command {
				case "sessions.create":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"status": "ok", "message": sessionCreatedMessage, "session": got.Session,
					})
				case "request.get":
					_, _ = w.Write([]byte(tc.body))
				case "sessions.destroy":
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				}
			}))
			t.Cleanup(srv.Close)
			_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFetchRejectsExistingSessionWithoutDestroyingIt(t *testing.T) {
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		_ = json.NewDecoder(r.Body).Decode(&got)
		commands = append(commands, got.Command)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok", "message": sessionAlreadyExistsError, "session": got.Session,
		})
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
	if err == nil || !strings.Contains(err.Error(), sessionAlreadyExistsError) {
		t.Fatalf("error = %v, want session collision", err)
	}
	if strings.Join(commands, ",") != "sessions.create" {
		t.Errorf("commands = %v, existing session must not be used or destroyed", commands)
	}
}

func TestFetchCleansUpAfterAmbiguousCreateFailure(t *testing.T) {
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		_ = json.NewDecoder(r.Body).Decode(&got)
		commands = append(commands, got.Command)
		if got.Command == "sessions.create" {
			_, _ = w.Write([]byte(`{`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","message":"Error: The session doesn't exist."}`))
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), srv.URL, "https://example.test", DefaultMaxTimeout, DefaultVimmTabsTillVerify)
	if err == nil || !strings.Contains(err.Error(), "decode FlareSolverr response") {
		t.Fatalf("error = %v, want original malformed-create error", err)
	}
	if strings.Join(commands, ",") != "sessions.create,sessions.destroy" {
		t.Errorf("commands = %v, ambiguous create must be cleaned up", commands)
	}
}
