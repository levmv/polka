package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	coverSearchMaxResults   = 24
	coverSearchMinWidth     = 350
	coverSearchTokenTTL     = 20 * time.Minute
	coverSearchMaxResponse  = 8 << 20
	coverSearchBrowserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
)

var (
	coverSearchVQDPattern  = regexp.MustCompile(`vqd=["']([^"']+)["']`)
	fallbackCoverSearchKey = newCoverSearchKey()
	// SSRF guard for public cover-preview fetches. validatePublicImageIP already
	// rejects the ranges covered by netip's predicates (loopback/private/
	// link-local/unspecified/multicast); this list is only the remaining
	// special-purpose IPv4 space that is still global-unicast by stdlib rules.
	publicImageSpecialIPRanges = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}
)

type coverSearchApplyRequest struct {
	Token string `json:"token"`
}

type coverSearchResultDTO struct {
	Token      string `json:"token"`
	PreviewURL string `json:"preview_url"`
	Source     string `json:"source"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type coverSearchTokenPayload struct {
	SourceURL  string `json:"source_url"`
	PreviewURL string `json:"preview_url"`
	Source     string `json:"source"`
	ExpiresAt  int64  `json:"exp"`
}

type coverSearchCandidate struct {
	SourceURL  string
	PreviewURL string
	Source     string
	Width      int
	Height     int
}

type duckDuckGoImageResponse struct {
	Results []duckDuckGoImageResult `json:"results"`
}

type duckDuckGoImageResult struct {
	Image     string `json:"image"`
	Thumbnail string `json:"thumbnail"`
	URL       string `json:"url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type fetchedPublicImage struct {
	Bytes       validatedCoverBytes
	ContentType string
}

func (s *Server) handleAPICoverSearch(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	title := strings.TrimSpace(r.URL.Query().Get("title"))
	author := strings.TrimSpace(r.URL.Query().Get("author"))
	if title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}

	results, err := s.searchWebCoverCandidates(r.Context(), title, author)
	if err != nil {
		log.Printf("cover web search %q/%q: %v", title, author, err)
		http.Error(w, "Cover provider unavailable", http.StatusBadGateway)
		return
	}

	out := make([]coverSearchResultDTO, 0, len(results))
	for _, result := range results {
		payload := coverSearchTokenPayload{
			SourceURL:  result.SourceURL,
			PreviewURL: result.PreviewURL,
			Source:     result.Source,
			ExpiresAt:  time.Now().Add(coverSearchTokenTTL).Unix(),
		}
		token, err := s.signCoverSearchToken(payload)
		if err != nil {
			serverError(w, err)
			return
		}
		out = append(out, coverSearchResultDTO{
			Token:      token,
			PreviewURL: "/api/books/" + url.PathEscape(workID) + "/cover-search/preview?token=" + url.QueryEscape(token),
			Source:     result.Source,
			Width:      result.Width,
			Height:     result.Height,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPICoverSearchPreview(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	payload, err := s.verifyCoverSearchToken(r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	previewURL := payload.PreviewURL
	if previewURL == "" {
		previewURL = payload.SourceURL
	}
	image, err := s.fetchPublicImage(r.Context(), previewURL)
	if err != nil {
		log.Printf("cover web preview %s: %v", redactedURLForLog(previewURL), err)
		http.Error(w, "Remote preview unavailable", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "cover-preview", time.Now(), bytes.NewReader(image.Bytes))
}

func (s *Server) handleAPICoverSearchApply(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	var req coverSearchApplyRequest
	if !readLimitedJSON(w, r, &req, 32<<10) {
		return
	}
	payload, err := s.verifyCoverSearchToken(req.Token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	image, err := s.fetchPublicImage(r.Context(), payload.SourceURL)
	if err != nil {
		log.Printf("cover web apply %s: %v", redactedURLForLog(payload.SourceURL), err)
		http.Error(w, "Remote image unavailable", http.StatusBadRequest)
		return
	}
	s.storeCoverAndReturnBook(w, r, workID, image.Bytes)
}

func (s *Server) searchWebCoverCandidates(ctx context.Context, title, author string) ([]coverSearchCandidate, error) {
	queries := webCoverSearchQueries(title, author)
	results := make([]coverSearchCandidate, 0, coverSearchMaxResults)
	seen := make(map[string]bool)
	var lastErr error
	var searched bool

	for _, query := range queries {
		candidates, err := s.searchDuckDuckGoCoverImages(ctx, query)
		if err != nil {
			lastErr = err
			continue
		}
		searched = true
		for _, candidate := range candidates {
			key := candidate.SourceURL
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			results = append(results, candidate)
			if len(results) >= coverSearchMaxResults {
				return results, nil
			}
		}
	}
	if len(results) == 0 && !searched && lastErr != nil {
		return nil, lastErr
	}
	return results, nil
}

func webCoverSearchQueries(title, author string) []string {
	base := strings.TrimSpace(title)
	if author != "" {
		base += " " + strings.TrimSpace(author)
	}
	base = strings.TrimSpace(base + " book cover")
	return []string{
		base + " (site:amazon.com OR site:goodreads.com)",
		base,
	}
}

func (s *Server) searchDuckDuckGoCoverImages(ctx context.Context, query string) ([]coverSearchCandidate, error) {
	searchURL := duckDuckGoSearchPageURL(query)
	page, err := s.fetchCoverSearchURL(ctx, searchURL, "")
	if err != nil {
		return nil, err
	}
	vqd := extractDuckDuckGoVQD(page)
	if vqd == "" {
		return nil, errors.New("missing DuckDuckGo image token")
	}

	apiURL := duckDuckGoImagesURL(query, vqd)
	body, err := s.fetchCoverSearchURL(ctx, apiURL, searchURL)
	if err != nil {
		return nil, err
	}
	var response duckDuckGoImageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	out := make([]coverSearchCandidate, 0, len(response.Results))
	for _, result := range response.Results {
		candidate, ok := duckDuckGoCoverCandidate(result)
		if ok {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func duckDuckGoCoverCandidate(result duckDuckGoImageResult) (coverSearchCandidate, bool) {
	sourceURL := strings.TrimSpace(result.Image)
	if sourceURL == "" {
		return coverSearchCandidate{}, false
	}
	if _, err := parsePublicImageURL(sourceURL); err != nil {
		return coverSearchCandidate{}, false
	}
	previewURL := strings.TrimSpace(result.Thumbnail)
	if previewURL == "" {
		previewURL = sourceURL
	}
	if _, err := parsePublicImageURL(previewURL); err != nil {
		previewURL = sourceURL
	}
	if result.Width < coverSearchMinWidth || result.Height <= 0 || result.Width >= result.Height {
		return coverSearchCandidate{}, false
	}
	return coverSearchCandidate{
		SourceURL:  sourceURL,
		PreviewURL: previewURL,
		Source:     webCoverSourceLabel(result.URL, sourceURL),
		Width:      result.Width,
		Height:     result.Height,
	}, true
}

func duckDuckGoSearchPageURL(query string) string {
	u := url.URL{Scheme: "https", Host: "duckduckgo.com", Path: "/"}
	q := u.Query()
	q.Set("q", query)
	q.Set("iax", "images")
	q.Set("ia", "images")
	q.Set("iar", "images")
	q.Set("iaf", "size:Large,layout:Tall")
	u.RawQuery = q.Encode()
	return u.String()
}

func duckDuckGoImagesURL(query, vqd string) string {
	u := url.URL{Scheme: "https", Host: "duckduckgo.com", Path: "/i.js"}
	q := u.Query()
	q.Set("o", "json")
	q.Set("q", query)
	q.Set("vqd", vqd)
	q.Set("iar", "images")
	q.Set("iaf", "size:Large,layout:Tall")
	u.RawQuery = q.Encode()
	return u.String()
}

func extractDuckDuckGoVQD(body []byte) string {
	match := coverSearchVQDPattern.FindSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return string(match[1])
}

func (s *Server) fetchCoverSearchURL(ctx context.Context, rawURL, referer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", coverSearchBrowserAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	client := s.coverSearchClient
	if client == nil {
		client = defaultCoverSearchClient()
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("cover search provider returned " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, coverSearchMaxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > coverSearchMaxResponse {
		return nil, errors.New("cover search response is too large")
	}
	return data, nil
}

func defaultCoverSearchClient() *http.Client {
	// Unlike the image URLs returned by search, these request URLs are built
	// locally for fixed DuckDuckGo endpoints. Keep the ordinary transport here;
	// arbitrary result URLs cross the stricter boundary in fetchPublicImage.
	return &http.Client{Timeout: 10 * time.Second}
}

// Provider cover URLs in api_cover.go are HTTPS-only and host-allowlisted. Search
// results can point at any public image host, so this path instead validates
// every redirect before following it and verifies the connected peer address
// in safePublicImageDialContext to resist DNS rebinding. Keep the two policies
// distinct: neither is a fallback for the other.
func (s *Server) fetchPublicImage(ctx context.Context, rawURL string) (fetchedPublicImage, error) {
	u, err := parsePublicImageURL(rawURL)
	if err != nil {
		return fetchedPublicImage{}, err
	}
	client := s.publicImageClient
	if client == nil {
		client = defaultPublicImageClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fetchedPublicImage{}, err
	}
	req.Header.Set("User-Agent", remoteCoverUserAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.5")

	res, err := client.Do(req)
	if err != nil {
		return fetchedPublicImage{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fetchedPublicImage{}, errors.New("image host returned " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxCoverBytes+1))
	if err != nil {
		return fetchedPublicImage{}, err
	}
	if len(data) > maxCoverBytes {
		return fetchedPublicImage{}, errors.New("Cover image is too large")
	}
	coverBytes, err := validateCoverBytes(data)
	if err != nil {
		return fetchedPublicImage{}, errors.New("Invalid image format (must be " + coverFormatHint + ")")
	}
	return fetchedPublicImage{
		Bytes:       coverBytes,
		ContentType: imageContentType(data),
	}, nil
}

func defaultPublicImageClient() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           safePublicImageDialContext,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validatePublicImageURL(req.URL)
		},
	}
}

func safePublicImageDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		conn.Close()
		return nil, errors.New("Unsafe image host")
	}
	addr, ok := netip.AddrFromSlice(tcpAddr.IP)
	if !ok {
		conn.Close()
		return nil, errors.New("Unsafe image host")
	}
	if err := validatePublicImageIP(addr); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func parsePublicImageURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil || !u.IsAbs() {
		return nil, errors.New("Invalid image URL")
	}
	if err := validatePublicImageURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

func validatePublicImageURL(u *url.URL) error {
	if err := validatePublicImageURLCommon(u); err != nil {
		return err
	}
	return validatePublicImageHost(u.Hostname())
}

func validatePublicImageURLCommon(u *url.URL) error {
	if u == nil || !u.IsAbs() {
		return errors.New("Invalid image URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("Image URL must use HTTP or HTTPS")
	}
	if u.User != nil || u.Hostname() == "" {
		return errors.New("Invalid image URL")
	}
	return nil
}

func validatePublicImageHost(host string) error {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || strings.Contains(host, "%") {
		return errors.New("Unsafe image host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "local" || strings.HasSuffix(host, ".local") {
		return errors.New("Unsafe image host")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return validatePublicImageIP(addr)
	}
	return nil
}

func validatePublicImageIP(addr netip.Addr) error {
	addr = addr.Unmap()
	if !addr.IsValid() ||
		!addr.IsGlobalUnicast() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() {
		return errors.New("Unsafe image host")
	}
	for _, prefix := range publicImageSpecialIPRanges {
		if prefix.Contains(addr) {
			return errors.New("Unsafe image host")
		}
	}
	return nil
}

func imageContentType(data []byte) string {
	if detected := http.DetectContentType(data); strings.HasPrefix(strings.ToLower(detected), "image/") {
		return detected
	}
	return "application/octet-stream"
}

func webCoverSourceLabel(pageURL, imageURL string) string {
	for _, raw := range []string{pageURL, imageURL} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if strings.Contains(host, "amazon.") {
			return "Amazon"
		}
		if host == "goodreads.com" || strings.HasSuffix(host, ".goodreads.com") {
			return "Goodreads"
		}
		return host
	}
	return "Web"
}

// newCoverSearchKey creates the per-process HMAC key used to sign cover-search
// result tokens. Those tokens keep the preview/apply endpoints from becoming an
// open image proxy: the browser may fetch only URLs returned by this server's
// current search results. Restarting the server invalidates outstanding tokens,
// which is fine because they are short-lived UI state.
func newCoverSearchKey() []byte {
	key := make([]byte, 32)
	rand.Read(key)
	return key
}

func (s *Server) coverSearchSigningKey() []byte {
	if len(s.coverSearchKey) > 0 {
		return s.coverSearchKey
	}
	return fallbackCoverSearchKey
}

func (s *Server) signCoverSearchToken(payload coverSearchTokenPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.coverSearchSigningKey())
	mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

func (s *Server) verifyCoverSearchToken(token string) (coverSearchTokenPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return coverSearchTokenPayload{}, errors.New("Invalid cover token")
	}
	mac := hmac.New(sha256.New, s.coverSearchSigningKey())
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(actual, expected) {
		return coverSearchTokenPayload{}, errors.New("Invalid cover token")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return coverSearchTokenPayload{}, errors.New("Invalid cover token")
	}
	var payload coverSearchTokenPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return coverSearchTokenPayload{}, errors.New("Invalid cover token")
	}
	if payload.ExpiresAt < time.Now().Unix() {
		return coverSearchTokenPayload{}, errors.New("Cover token expired")
	}
	return payload, nil
}

func redactedURLForLog(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return "<invalid>"
	}
	return u.Redacted()
}
