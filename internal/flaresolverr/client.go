// Package flaresolverr provides the small part of the FlareSolverr API that
// Gamarr needs to read a Vimm vault page after its browser challenge.
package flaresolverr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultMaxTimeout matches Jackett's FlareSolverr default. Values are in
	// milliseconds because that is the unit used by the FlareSolverr API.
	DefaultMaxTimeout = 55_000
	// DefaultVimmTabsTillVerify is Vimm's manual-focus preset. Tab order is
	// layout-dependent, so deployments can override it through configuration.
	DefaultVimmTabsTillVerify = 37
	// FlareSolverr counts both its manual Turnstile click pauses and
	// waitInSeconds against maxTimeout. Keep a usable floor for that flow.
	MinMaxTimeout = 20_000
	// MaxMaxTimeout prevents overflowing time.Duration and keeps a typo from
	// tying up a download worker (and its browser) indefinitely.
	MaxMaxTimeout = 600_000

	MinTabsTillVerify = 1
	// FlareSolverr has no documented upper limit. This generous bound catches
	// accidental extra digits while still allowing unusual page layouts.
	MaxTabsTillVerify = 1_000

	// Vimm's interstitial submits itself after its browser check completes.
	// Ask FlareSolverr to wait before capturing the resulting page source.
	vimmWaitSeconds     = 5
	solverOverheadGrace = 15 * time.Second
	maxResponseSize     = 16 << 20
	connectivityTimeout = 10 * time.Second
)

type request struct {
	Command        string `json:"cmd"`
	URL            string `json:"url"`
	MaxTimeout     int    `json:"maxTimeout"`
	WaitInSeconds  int    `json:"waitInSeconds"`
	TabsTillVerify int    `json:"tabs_till_verify"`
}

type apiResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
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

// Fetch asks FlareSolverr to load targetURL and return its rendered HTML.
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
	payload, err := json.Marshal(request{
		Command:        "request.get",
		URL:            targetURL,
		MaxTimeout:     maxTimeout,
		WaitInSeconds:  vimmWaitSeconds,
		TabsTillVerify: tabsTillVerify,
	})
	if err != nil {
		return Solution{}, fmt.Errorf("encode FlareSolverr request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(payload))
	if err != nil {
		return Solution{}, fmt.Errorf("create FlareSolverr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gamarr/1.0")
	client := &http.Client{Timeout: requestTimeout(maxTimeout)}
	resp, err := client.Do(req)
	if err != nil {
		return Solution{}, fmt.Errorf("FlareSolverr request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return Solution{}, fmt.Errorf("read FlareSolverr response: %w", err)
	}
	if len(body) > maxResponseSize {
		return Solution{}, fmt.Errorf("FlareSolverr response exceeded %d MiB", maxResponseSize>>20)
	}
	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return Solution{}, fmt.Errorf("FlareSolverr returned HTTP %d", resp.StatusCode)
		}
		return Solution{}, fmt.Errorf("decode FlareSolverr response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.EqualFold(result.Status, "ok") {
		message := compactMessage(result.Message)
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return Solution{}, fmt.Errorf("FlareSolverr could not solve the page: %s", message)
	}
	if result.Solution.Response == "" {
		return Solution{}, fmt.Errorf("FlareSolverr returned an empty page")
	}
	return Solution{
		Status:   result.Solution.Status,
		Response: result.Solution.Response,
	}, nil
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
