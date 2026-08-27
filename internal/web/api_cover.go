package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/storage"
)

const (
	maxCoverBytes        = 10 << 20
	remoteCoverUserAgent = "github.com/levmv/polka/0 (personal library cover lookup)"
	coverFormatHint      = "JPEG, PNG, GIF, or WebP"
)

type coverURLRequest struct {
	URL string `json:"url"`
}

type generatedCoverPreviewRequest struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	Seed   int    `json:"seed"`
	Style  string `json:"style"`
}

type validatedCoverBytes []byte

func validateCoverBytes(coverBytes []byte) (validatedCoverBytes, error) {
	if _, err := covers.Validate(coverBytes); err != nil {
		return nil, err
	}
	return validatedCoverBytes(coverBytes), nil
}

// storeCoverBytes owns the complete cover mutation. Callers finish all remote
// IO and image validation first; from staging through DB commit, original-file
// promotion, and derived-cache invalidation this method holds the same storage
// slot as write-back/import/relayout. A write-back therefore cannot observe a
// new cover revision while the old original is still on disk.
func (s *Server) storeCoverBytes(ctx context.Context, workID string, coverBytes validatedCoverBytes) error {
	releaseStorageSlot, err := s.acquireStorageWorkSlot(ctx)
	if err != nil {
		return err
	}
	defer releaseStorageSlot()

	coverPath := covers.OriginalPath(workID)
	root := s.dataRoot()
	err = storage.Place(root, coverPath, bytes.NewReader([]byte(coverBytes)), func() error {
		return s.db.Transact(ctx, func(tx *sql.Tx) error {
			var overrides sql.NullString
			if err := tx.QueryRow(`
				SELECT manual_overrides
				FROM works
				WHERE id = ? AND deleted_at IS NULL
			`, workID).Scan(&overrides); err != nil {
				return err
			}

			overrideMap := bookmeta.ParseOverrides(overrides.String)
			overrideMap["cover"] = true
			_, err := tx.Exec(`
				UPDATE works
				SET cover_version = cover_version + 1,
				    metadata_rev = metadata_rev + 1,
				    manual_overrides = ?,
				    updated_at = unixepoch()
				WHERE id = ?
			`, bookmeta.MarshalOverrides(overrideMap), workID)
			return err
		})
	})
	if err != nil {
		return err
	}

	covers.RemoveDerived(root, workID)
	return nil
}

func (s *Server) storeCoverAndReturnBook(w http.ResponseWriter, r *http.Request, workID string, coverBytes validatedCoverBytes) {
	if err := s.storeCoverBytes(r.Context(), workID, coverBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			serverError(w, err)
		}
		return
	}

	s.handleAPIBookDetailReturn(w, r, workID)
}

func (s *Server) handleAPICoverUpload(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	if !parseLimitedMultipartForm(w, r, maxCoverBytes, maxCoverBytes) {
		return
	}

	file, _, err := r.FormFile("cover")
	if err != nil {
		http.Error(w, "Missing cover field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	coverBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Uploaded image could not be read", http.StatusBadRequest)
		return
	}
	validCover, err := validateCoverBytes(coverBytes)
	if err != nil {
		http.Error(w, "Invalid image format (must be "+coverFormatHint+")", http.StatusBadRequest)
		return
	}

	s.storeCoverAndReturnBook(w, r, workID, validCover)
}

func (s *Server) handleAPIGeneratedCoverPreview(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	var req generatedCoverPreviewRequest
	if !readLimitedJSON(w, r, &req, 32<<10) {
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}
	author := strings.TrimSpace(req.Author)

	enc, err := covers.GeneratedStyled(title, author, covers.VariantDisplay, covers.DefaultOptions(), req.Seed, req.Style)
	if err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Content-Type", enc.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "generated-cover.jpg", time.Now(), bytes.NewReader(enc.Bytes))
}

func (s *Server) handleAPICoverURL(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	var req coverURLRequest
	if !readLimitedJSON(w, r, &req, 16<<10) {
		return
	}

	coverBytes, err := s.fetchRemoteCover(r, req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.storeCoverAndReturnBook(w, r, workID, coverBytes)
}

func (s *Server) fetchRemoteCover(r *http.Request, rawURL string) (validatedCoverBytes, error) {
	u, err := parseRemoteCoverURL(rawURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", remoteCoverUserAgent)

	client := s.coverClient
	if client == nil {
		client = defaultRemoteCoverClient()
	}
	res, err := client.Do(req)
	if err != nil {
		log.Printf("download remote cover %s: %v", u.Redacted(), err)
		return nil, errors.New("Remote image unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		log.Printf("download remote cover %s: unexpected status %s", u.Redacted(), res.Status)
		return nil, errors.New("Remote image unavailable")
	}

	data, err := io.ReadAll(io.LimitReader(res.Body, maxCoverBytes+1))
	if err != nil {
		log.Printf("read remote cover %s: %v", u.Redacted(), err)
		return nil, errors.New("Remote image could not be read")
	}
	if len(data) > maxCoverBytes {
		return nil, errors.New("Cover image is too large")
	}
	coverBytes, err := validateCoverBytes(data)
	if err != nil {
		log.Printf("inspect remote cover %s: %v", u.Redacted(), err)
		return nil, errors.New("Invalid image format (must be " + coverFormatHint + ")")
	}
	return coverBytes, nil
}

func defaultRemoteCoverClient() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validateRemoteCoverRedirectURL(req.URL, via)
		},
	}
}

func parseRemoteCoverURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil || !u.IsAbs() {
		return nil, errors.New("Invalid cover URL")
	}
	if err := validateRemoteCoverURLCommon(u); err != nil {
		return nil, err
	}
	if !isRemoteCoverProviderHost(u.Hostname()) {
		return nil, errors.New("Unsupported cover URL host")
	}
	return u, nil
}

func validateRemoteCoverRedirectURL(u *url.URL, via []*http.Request) error {
	if err := validateRemoteCoverURLCommon(u); err != nil {
		return err
	}
	if isRemoteCoverProviderHost(u.Hostname()) {
		return nil
	}
	if len(via) > 0 && isOpenLibraryArchiveCoverRedirect(via[0].URL, u) {
		return nil
	}
	return errors.New("Unsupported cover URL host")
}

func validateRemoteCoverURLCommon(u *url.URL) error {
	if u == nil || !u.IsAbs() {
		return errors.New("Invalid cover URL")
	}
	if u.Scheme != "https" {
		return errors.New("Cover URL must use HTTPS")
	}
	if u.User != nil {
		return errors.New("Invalid cover URL")
	}
	return nil
}

func isRemoteCoverProviderHost(host string) bool {
	switch strings.ToLower(host) {
	case "covers.openlibrary.org", "books.google.com", "books.googleusercontent.com":
		return true
	default:
		return false
	}
}

func isOpenLibraryArchiveCoverRedirect(original, redirected *url.URL) bool {
	if original == nil || redirected == nil {
		return false
	}
	if strings.ToLower(original.Hostname()) != "covers.openlibrary.org" {
		return false
	}
	host := strings.ToLower(redirected.Hostname())
	return host == "archive.org" || strings.HasSuffix(host, ".archive.org")
}
