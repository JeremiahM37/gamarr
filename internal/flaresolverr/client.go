// Package flaresolverr provides the small part of the FlareSolverr API that
// Gamarr needs to read a Vimm vault page after its browser challenge.
package flaresolverr

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultMaxTimeout leaves margin above the observed Vimm Turnstile solve.
	// Values are milliseconds because that is the FlareSolverr API unit.
	DefaultMaxTimeout = 55_000
	// DefaultVimmTabsTillVerify is Vimm's manual-focus preset. Tab order is
	// layout-dependent, so deployments can override it through configuration.
	DefaultVimmTabsTillVerify = 74
	// Twenty seconds is sufficient for the verified Vimm Turnstile flow while
	// still leaving enough time for FlareSolverr to drive the browser.
	MinMaxTimeout = 20_000
	// MaxMaxTimeout bounds the supported solve window so a typo cannot tie up a
	// download worker (and its browser) longer than the recommended maximum.
	MaxMaxTimeout = 55_000

	MinTabsTillVerify = 1
	// FlareSolverr has no documented upper limit. This generous bound catches
	// accidental extra digits while still allowing unusual page layouts.
	MaxTabsTillVerify = 1_000

	// Vimm's interstitial submits itself after its browser check completes.
	// Ask FlareSolverr to wait before capturing the resulting page source.
	vimmWaitSeconds           = 5
	vimmFollowupWaitSeconds   = 2
	solverOverheadGrace       = 15 * time.Second
	sessionCleanupTimeout     = 10 * time.Second
	maxResponseSize           = 16 << 20
	connectivityTimeout       = 10 * time.Second
	sessionCreatedMessage     = "Session created successfully."
	sessionAlreadyExistsError = "Session already exists."
	sessionMissingError       = "The session doesn't exist."
)

type request struct {
	Command        string `json:"cmd"`
	URL            string `json:"url,omitempty"`
	Session        string `json:"session,omitempty"`
	MaxTimeout     int    `json:"maxTimeout,omitempty"`
	WaitInSeconds  int    `json:"waitInSeconds,omitempty"`
	TabsTillVerify *int   `json:"tabs_till_verify,omitempty"`
}

type apiResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Session  string `json:"session"`
	Solution struct {
		Status   int    `json:"status"`
		Response string `json:"response"`
	} `json:"solution"`
}

// Solution is the browser result returned by FlareSolverr.
type Solution struct {
	Status   int
	Response string
}

// ServiceInfo is returned by FlareSolverr's lightweight root endpoint.
type ServiceInfo struct {
	Version string
}

type apiClient struct {
	endpointURL string
	httpClient  *http.Client
}

type apiError struct {
	Command    string
	HTTPStatus int
	Status     string
	Message    string
}

func (e *apiError) Error() string {
	message := compactMessage(e.Message)
	if message == "" {
		message = fmt.Sprintf("HTTP %d", e.HTTPStatus)
	}
	return fmt.Sprintf("FlareSolverr %s failed: %s", e.Command, message)
}

// NormalizeAPIURL validates a FlareSolverr base URL and removes trailing
// slashes. An empty URL is valid and means the integration is disabled.
func NormalizeAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("FlareSolverr API URL must be an absolute HTTP or HTTPS URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("FlareSolverr API URL must use HTTP or HTTPS")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("FlareSolverr API URL must not contain a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func endpoint(apiURL string) (string, error) {
	base, err := NormalizeAPIURL(apiURL)
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", fmt.Errorf("FlareSolverr API URL is not configured")
	}
	if strings.HasSuffix(base, "/v1") {
		return base, nil
	}
	return base + "/v1", nil
}

func healthEndpoint(apiURL string) (string, error) {
	base, err := NormalizeAPIURL(apiURL)
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", fmt.Errorf("FlareSolverr API URL is not configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse FlareSolverr API URL: %w", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/v1")
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath = ""
	return u.String(), nil
}

// Check verifies that the configured URL reaches FlareSolverr without
// launching a browser or requesting a protected site.
func Check(ctx context.Context, apiURL string) (ServiceInfo, error) {
	healthURL, err := healthEndpoint(apiURL)
	if err != nil {
		return ServiceInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("create FlareSolverr health request: %w", err)
	}
	req.Header.Set("User-Agent", "Gamarr/1.0")
	resp, err := (&http.Client{Timeout: connectivityTimeout}).Do(req)
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("FlareSolverr connection failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("read FlareSolverr health response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ServiceInfo{}, fmt.Errorf("FlareSolverr returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Message string `json:"msg"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return ServiceInfo{}, fmt.Errorf("decode FlareSolverr health response: %w", err)
	}
	if !strings.Contains(strings.ToLower(result.Message), "flaresolverr") {
		return ServiceInfo{}, fmt.Errorf("configured URL did not return a FlareSolverr health response")
	}
	return ServiceInfo{Version: result.Version}, nil
}

// Fetch asks FlareSolverr to load targetURL in a temporary browser session and
// return its rendered HTML. Vimm can submit Turnstile successfully while
// FlareSolverr is still inspecting the old document. FlareSolverr 3.5.0 then
// reports a stale-element error, so retrieve the navigated page in the same
// session without trying to focus the challenge a second time.
func Fetch(ctx context.Context, apiURL, targetURL string, maxTimeout, tabsTillVerify int) (Solution, error) {
	if err := ValidateMaxTimeout(maxTimeout); err != nil {
		return Solution{}, err
	}
	if err := ValidateTabsTillVerify(tabsTillVerify); err != nil {
		return Solution{}, err
	}
	endpointURL, err := endpoint(apiURL)
	if err != nil {
		return Solution{}, err
	}
	client := apiClient{
		endpointURL: endpointURL,
		httpClient:  &http.Client{Timeout: requestTimeout(maxTimeout)},
	}
	sessionID, err := newSessionID()
	if err != nil {
		return Solution{}, fmt.Errorf("create FlareSolverr session ID: %w", err)
	}
	cleanupSession := true
	defer func() {
		if !cleanupSession {
			return
		}
		// The request may have created its browser even if its response was lost,
		// and the caller context may already be canceled. Always make one bounded
		// cleanup attempt using the caller-owned session ID.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), sessionCleanupTimeout)
		defer cancel()
		if _, cleanupErr := client.call(cleanupCtx, request{Command: "sessions.destroy", Session: sessionID}); cleanupErr != nil && !isMissingSession(cleanupErr) {
			slog.Warn("Could not destroy temporary FlareSolverr session", "session", sessionID, "error", cleanupErr)
		}
	}()
	created, err := client.call(ctx, request{Command: "sessions.create", Session: sessionID})
	if err != nil {
		if isSessionAlreadyExists(err) {
			cleanupSession = false
		}
		return Solution{}, fmt.Errorf("create FlareSolverr session: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(created.Message), sessionAlreadyExistsError) {
		cleanupSession = false
		return Solution{}, fmt.Errorf("create FlareSolverr session: %s", sessionAlreadyExistsError)
	}
	if created.Message != sessionCreatedMessage || created.Session != sessionID {
		return Solution{}, fmt.Errorf(
			"FlareSolverr returned an invalid session-create response (message %q, session %q)",
			created.Message, created.Session,
		)
	}

	first, err := client.call(ctx, request{
		Command:        "request.get",
		URL:            targetURL,
		Session:        sessionID,
		MaxTimeout:     maxTimeout,
		WaitInSeconds:  vimmWaitSeconds,
		TabsTillVerify: &tabsTillVerify,
	})
	if err == nil {
		return solutionFromResponse(first)
	}
	if !isStaleElementReference(err) {
		return Solution{}, err
	}
	if err := ctx.Err(); err != nil {
		return Solution{}, fmt.Errorf("retrieve Vimm page after Turnstile navigation: %w", err)
	}

	second, err := client.call(ctx, request{
		Command:       "request.get",
		URL:           targetURL,
		Session:       sessionID,
		MaxTimeout:    maxTimeout,
		WaitInSeconds: vimmFollowupWaitSeconds,
	})
	if err != nil {
		return Solution{}, fmt.Errorf("retrieve Vimm page after Turnstile navigation: %w", err)
	}
	return solutionFromResponse(second)
}

func (c apiClient) call(ctx context.Context, payload request) (apiResponse, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return apiResponse{}, fmt.Errorf("encode FlareSolverr request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return apiResponse{}, fmt.Errorf("create FlareSolverr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gamarr/1.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return apiResponse{}, fmt.Errorf("FlareSolverr %s request failed: %w", payload.Command, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return apiResponse{}, fmt.Errorf("read FlareSolverr response: %w", err)
	}
	if len(body) > maxResponseSize {
		return apiResponse{}, fmt.Errorf("FlareSolverr response exceeded %d MiB", maxResponseSize>>20)
	}
	var result apiResponse
	decodeErr := json.Unmarshal(body, &result)
	if decodeErr != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			message := compactMessage(string(body))
			if message == "" {
				message = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			return apiResponse{}, fmt.Errorf("FlareSolverr %s failed: %s", payload.Command, message)
		}
		return apiResponse{}, fmt.Errorf("decode FlareSolverr response: %w", decodeErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.EqualFold(result.Status, "ok") {
		return apiResponse{}, &apiError{
			Command: payload.Command, HTTPStatus: resp.StatusCode,
			Status: result.Status, Message: result.Message,
		}
	}
	return result, nil
}

func solutionFromResponse(result apiResponse) (Solution, error) {
	if result.Solution.Response == "" {
		return Solution{}, fmt.Errorf("FlareSolverr returned an empty page")
	}
	return Solution{
		Status:   result.Solution.Status,
		Response: result.Solution.Response,
	}, nil
}

func newSessionID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "gamarr-vimm-" + hex.EncodeToString(random[:]), nil
}

func isStaleElementReference(err error) bool {
	var responseErr *apiError
	if !errors.As(err, &responseErr) ||
		responseErr.Command != "request.get" ||
		responseErr.HTTPStatus != http.StatusInternalServerError ||
		!strings.EqualFold(responseErr.Status, "error") {
		return false
	}
	message := strings.ToLower(responseErr.Message)
	return strings.Contains(message, "stale element reference") ||
		strings.Contains(message, "staleelementreferenceexception")
}

func isSessionAlreadyExists(err error) bool {
	var responseErr *apiError
	return errors.As(err, &responseErr) &&
		responseErr.Command == "sessions.create" &&
		strings.EqualFold(strings.TrimSpace(responseErr.Message), sessionAlreadyExistsError)
}

func isMissingSession(err error) bool {
	var responseErr *apiError
	return errors.As(err, &responseErr) &&
		responseErr.Command == "sessions.destroy" &&
		strings.Contains(strings.ToLower(responseErr.Message), strings.ToLower(sessionMissingError))
}

// ValidateMaxTimeout applies the same bound to startup configuration and
// direct client use.
func ValidateMaxTimeout(maxTimeout int) error {
	if maxTimeout < MinMaxTimeout {
		return fmt.Errorf("FlareSolverr max timeout must be at least %d ms", MinMaxTimeout)
	}
	if maxTimeout > MaxMaxTimeout {
		return fmt.Errorf("FlareSolverr max timeout must be at most %d ms", MaxMaxTimeout)
	}
	return nil
}

// ValidateTabsTillVerify rejects values that cannot move focus to a control.
// FlareSolverr itself does not publish an upper bound; its maxTimeout remains
// the guard against an excessively large intentional override.
func ValidateTabsTillVerify(tabsTillVerify int) error {
	if tabsTillVerify < MinTabsTillVerify {
		return fmt.Errorf("FlareSolverr tabs till verify must be at least %d", MinTabsTillVerify)
	}
	if tabsTillVerify > MaxTabsTillVerify {
		return fmt.Errorf("FlareSolverr tabs till verify must be at most %d", MaxTabsTillVerify)
	}
	return nil
}

func requestTimeout(maxTimeout int) time.Duration {
	// FlareSolverr applies maxTimeout to the complete browser operation,
	// including waitInSeconds. Browser startup/cleanup happens outside that
	// watchdog, so leave a bounded margin for it and response transfer.
	return time.Duration(maxTimeout)*time.Millisecond + solverOverheadGrace
}

func compactMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 300 {
		message = message[:300] + "..."
	}
	return message
}
