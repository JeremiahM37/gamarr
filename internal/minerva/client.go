package minerva

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var bundleLink = regexp.MustCompile(`(?i)<a\s+[^>]*?href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
var bundleName = regexp.MustCompile(`^Minerva_Myrient_v([0-9]+)\.([0-9]+)/?$`)

func discoverBundle(body []byte) (string, error) {
	var bestMajor, bestMinor uint64
	var version string
	for _, link := range bundleLink.FindAllSubmatch(body, -1) {
		var href string
		for _, value := range link[1:] {
			if len(value) > 0 {
				href = html.UnescapeString(string(value))
				break
			}
		}
		parts := bundleName.FindStringSubmatch(href)
		if parts == nil {
			continue
		}
		major, errMajor := strconv.ParseUint(parts[1], 10, 64)
		minor, errMinor := strconv.ParseUint(parts[2], 10, 64)
		if errMajor != nil || errMinor != nil {
			continue
		}
		candidate := "v" + parts[1] + "." + parts[2]
		if version == "" || major > bestMajor || (major == bestMajor && (minor > bestMinor || (minor == bestMinor && candidate < version))) {
			bestMajor, bestMinor, version = major, minor, candidate
		}
	}
	if version == "" {
		return "", fmt.Errorf("minerva: no bundle directory links found")
	}
	return version, nil
}

func collectionURL(assetsURL, version, browsePath string) (string, error) {
	name := strings.ReplaceAll(strings.TrimRight(browsePath, "/"), "/", " - ")
	directory, err := url.JoinPath(assetsURL, "Minerva_Myrient_"+version)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(directory)
	if err != nil {
		return "", err
	}
	filename := "Minerva_Myrient - " + name + ".torrent"
	escapedPath := strings.TrimRight(u.EscapedPath(), "/") + "/" + url.PathEscape(filename)
	u.Path = strings.TrimRight(u.Path, "/") + "/" + filename
	u.RawPath = escapedPath
	return u.String(), nil
}

func (s *Service) get(ctx context.Context, endpoint, etag, lastModified string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Gamarr/1.0")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	return s.client.Do(req)
}

func bodyHash(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }

func readResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("minerva: GET %s: HTTP %d", resp.Request.URL, resp.StatusCode)
	}
	const maxBody = 256 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("minerva: response exceeds 256 MiB")
	}
	return body, nil
}
