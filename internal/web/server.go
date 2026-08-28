package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/levmv/polka/internal/bootstrap"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/fsprofile"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/metalookup"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/workslot"
	"github.com/levmv/polka/internal/writeback"
)

type Server struct {
	db                *db.DB
	dataDir           string
	storageRoot       storage.Root
	ingester          *ingest.Service
	ingestCancel      context.CancelFunc
	ingestDone        <-chan struct{}
	ingestMu          sync.Mutex
	writebacker       *writeback.Service
	storageQueue      *workslot.Queue
	background        *taskGroup
	deliveryWake      chan struct{}
	deliveryTransport deliveryTransport
	sessions          *sessionStore
	metadata          metalookup.Registry
	coverClient       *http.Client
	coverSearchClient *http.Client
	publicImageClient *http.Client
	passwordAuthOnce  sync.Once
	passwordAuthSlots chan struct{}
	conversionOnce    sync.Once
	conversionSlots   chan struct{}
	// Per-process HMAC key for ephemeral cover-search preview/apply tokens.
	// A restart invalidates outstanding search result tokens by design.
	coverSearchKey []byte
}

// Config holds the parameters for Serve. AdminUser/AdminPassword bootstrap the
// first account on an un-bootstrapped library; if they are empty the first-run
// setup page handles it instead.
type Config struct {
	DataDir       string
	Addr          string
	AdminUser     string
	AdminPassword string
}

// ctxKey is the unexported type for request-context keys, avoiding collisions
// with values stored by other packages.
type ctxKey int

const (
	userIDKey ctxKey = iota
	userKey
	koboConnectionIDKey
)

// withUserID carries the identity shared by every authentication path, including
// token/basic-auth protocols that do not pass through the normal route gate.
func withUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// withUser adds the full account after route has loaded it and checked the
// minimum role. Protected handlers may therefore trust contextUser; code shared
// with protocol endpoints should use UserID instead.
func withUser(ctx context.Context, user *db.User) context.Context {
	if user == nil {
		return ctx
	}
	return context.WithValue(ctx, userKey, user)
}

// UserID returns the authenticated user's id for a request, or "" before the
// auth middleware has accepted a session.
func UserID(ctx context.Context) string {
	uid, _ := ctx.Value(userIDKey).(string)
	return uid
}

func contextUser(ctx context.Context) *db.User {
	user, _ := ctx.Value(userKey).(*db.User)
	return user
}

func withKoboConnectionID(ctx context.Context, connectionID string) context.Context {
	return context.WithValue(ctx, koboConnectionIDKey, connectionID)
}

func koboConnectionID(ctx context.Context) string {
	connectionID, _ := ctx.Value(koboConnectionIDKey).(string)
	return connectionID
}

func Serve(ctx context.Context, cfg Config) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	database, err := bootstrap.EnsureLibrary(cfg.DataDir)
	if err != nil {
		return err
	}
	// Teardown order is load-bearing: request shutdown below joins handlers,
	// then later defers join ingest and background work before releasing the
	// writer lease and closing the database.
	defer database.Close()

	if err := bootstrapAdmin(database, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return err
	}
	lease, err := db.AcquireWriterLease(ctx, database, db.NewWriterLeaseOwner("serve"), false)
	if err != nil {
		return err
	}
	defer lease.Release(context.Background())

	root, err := openServeBooksRoot(database, cfg.DataDir)
	if err != nil {
		return err
	}
	if info := fsprofile.Detect(root.Path); info.IsNetwork() {
		log.Printf("INFO: the books folder is on network filesystem %q: %s", info.TypeOrUnknown(), root.Path)
	}
	background := newTaskGroup(context.Background())
	defer background.Stop()
	storageQueue := workslot.New()
	if err := database.RecoverDeliveryJobs(); err != nil {
		return err
	}

	// Build the metadata registry and outbound cover client once, not per
	// request: a long-lived http.Client keeps connections alive, so reviewing
	// several metadata candidates (each a lazy Open Library description fetch) or
	// downloading covers reuses the connection instead of re-handshaking. The
	// per-request fallbacks in metadataRegistry()/handleAPICoverURL remain for
	// tests that construct a Server directly.
	s := &Server{
		db:                database,
		dataDir:           cfg.DataDir,
		storageRoot:       root,
		storageQueue:      storageQueue,
		background:        background,
		deliveryWake:      make(chan struct{}, 1),
		deliveryTransport: smtpDeliveryTransport{},
		sessions:          newSessionStore(database),
		metadata:          metalookup.NewCachedRegistry(nil),
		coverClient:       defaultRemoteCoverClient(),
		coverSearchClient: defaultCoverSearchClient(),
		publicImageClient: defaultPublicImageClient(),
		coverSearchKey:    newCoverSearchKey(),
	}
	ingestConfig, err := ingest.OpenConfig(database.DB, cfg.DataDir)
	if err != nil {
		return err
	}
	if err := s.configureIngest(ingestConfig); err != nil {
		return err
	}
	defer s.stopIngester()

	leaseErrc := make(chan error, 1)
	if !background.Go(func(ctx context.Context) {
		if err := lease.RunHeartbeat(ctx, db.DefaultWriterLeaseTTL/3); err != nil {
			leaseErrc <- err
		}
	}) {
		return fmt.Errorf("start writer lease heartbeat: server is stopping")
	}
	s.writebacker = writeback.NewService(database, root, writeback.ServiceOptions{
		WorkQueue: s.storageQueue,
		CoverRoot: s.dataRoot(),
		Logf:      log.Printf,
	})
	if !background.Go(s.writebacker.Start) {
		return fmt.Errorf("start metadata write-back: server is stopping")
	}
	if !background.Go(s.runDeliveryWorker) {
		return fmt.Errorf("start delivery worker: server is stopping")
	}

	mux, err := s.routes()
	if err != nil {
		return err
	}
	requests := newTaskGroup(context.Background())
	handler := requests.Wrap(s.authMiddleware(mux))

	// A bare `go build` embeds only static/placeholder.txt (esbuild never ran),
	// so a missing or empty app.js is the real "not built" signal. Don't sniff the
	// bundle contents — the built bundle legitimately contains the word
	// "placeholder" (a search-input attribute), which made this warning fire falsely.
	bundle, err := staticFS.ReadFile("static/app.js")
	if err != nil || len(bundle) == 0 {
		log.Println("WARNING: frontend bundle not built — run `make build`")
	}

	server := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		BaseContext: func(net.Listener) context.Context {
			return requests.Context()
		},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	log.Printf("Server listening on http://%s (data: %s)", listener.Addr(), cfg.DataDir)

	errc := make(chan error, 1)
	go func() {
		errc <- server.Serve(listener)
	}()

	var shutdownErr error
	forceShutdown := false
	select {
	case err := <-errc:
		closeErr := forceCloseHTTPServer(server, requests)
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-leaseErrc:
		shutdownErr = fmt.Errorf("writer lease lost: %w", err)
		forceShutdown = true
		// Another process may now own the storage mutation boundary. Cancel all
		// background mutation immediately and force active requests to stop below;
		// the ordinary signal path remains graceful.
		background.BeginStop()
	case <-ctx.Done():
	}

	log.Println("INFO: shutting down server")
	if forceShutdown {
		if err := forceCloseHTTPServer(server, requests); err != nil {
			return fmt.Errorf("shutdown server after writer lease loss: %w", err)
		}
	} else {
		if err := shutdownHTTPServer(server, requests, 10*time.Second); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}

func openServeBooksRoot(database *db.DB, dataDir string) (storage.Root, error) {
	configured, err := storage.RootConfigured(database.DB)
	if err != nil {
		return storage.Root{}, err
	}
	if configured {
		// A configured missing root is usually a dropped drive or mount. Do not
		// create it during startup; let reads degrade and write guards report it.
		return storage.OpenRoot(database.DB, dataDir)
	}
	root, err := storage.SaveRoot(database.DB, dataDir, "")
	if err != nil {
		return storage.Root{}, err
	}
	if err := storage.EnsureLayout(root); err != nil {
		return storage.Root{}, err
	}
	return root, nil
}

func (s *Server) managedRoot() storage.Root {
	if s.storageRoot.Path != "" {
		return s.storageRoot
	}
	return storage.NewRoot(s.dataDir)
}

// dataRoot resolves app-owned artifacts kept next to the database: cover
// originals (covers/) and the derived cover cache (cache/covers/). Unlike the
// books root it is always local and present, so cover writes never guard on a
// possibly-absent managed root.
func (s *Server) dataRoot() storage.Root {
	return storage.NewRoot(s.dataDir)
}

func (s *Server) configureIngest(cfg ingest.Config) error {
	var next *ingest.Service
	if cfg.Enabled {
		if err := ingest.EnsureLayout(cfg.Path); err != nil {
			return err
		}
		next = ingest.NewService(s.db, s.managedRoot(), cfg.Path, ingest.Options{
			DeleteSources: cfg.DeleteSources,
			ImportQueue:   s.storageQueue,
			CoverRoot:     s.dataRoot(),
			Logf:          log.Printf,
		})
		if s.background == nil {
			return fmt.Errorf("start ingest service: background lifecycle is not configured")
		}
	}

	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	s.stopIngesterLocked()
	if next == nil {
		return nil
	}

	ctx, cancel := context.WithCancel(s.background.Context())
	done := make(chan struct{})
	if !s.background.Go(func(context.Context) {
		defer close(done)
		next.Start(ctx)
	}) {
		cancel()
		return fmt.Errorf("start ingest service: server is stopping")
	}
	s.ingester = next
	s.ingestCancel = cancel
	s.ingestDone = done
	return nil
}

func (s *Server) stopIngester() {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	s.stopIngesterLocked()
}

func (s *Server) stopIngesterLocked() {
	if s.ingestCancel != nil {
		s.ingestCancel()
	}
	if s.ingestDone != nil {
		<-s.ingestDone
	}
	s.ingester = nil
	s.ingestCancel = nil
	s.ingestDone = nil
}

func (s *Server) currentIngester() *ingest.Service {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	return s.ingester
}

// routes builds the request multiplexer using Go 1.22+ method+path patterns, so
// each handler is reached only for the verb and path it serves (the mux returns
// 405/404 otherwise) and path parameters come from r.PathValue rather than
// manual prefix/suffix trimming. The returned mux is unauthenticated; Serve
// wraps it with authMiddleware.
func (s *Server) routes() (*http.ServeMux, error) {
	mux := http.NewServeMux()

	// UI routes
	s.route(mux, "GET /{$}", db.RoleReader, s.handleApp)
	s.route(mux, "GET /book/{id}", db.RoleReader, s.handleApp)
	s.route(mux, "GET /read/{id}", db.RoleReader, s.handleRead)
	s.route(mux, "GET /read/asset/{id}", db.RoleReader, s.handleReadAssetPage)
	s.route(mux, "GET /login", rolePublic, s.handleLogin)
	s.route(mux, "POST /login", rolePublic, s.handleLogin)
	s.route(mux, "POST /logout", db.RoleReader, s.handleLogout)
	s.route(mux, "GET /setup", rolePublic, s.handleSetup)
	s.route(mux, "POST /setup", rolePublic, s.handleSetup)
	s.route(mux, "GET /authors", db.RoleMember, s.handleApp)
	s.route(mux, "GET /cleanup", db.RoleMember, s.handleApp)
	s.route(mux, "GET /series", db.RoleReader, s.handleApp)
	s.route(mux, "GET /trash", db.RoleMember, s.handleApp)

	// OPDS routes
	s.route(mux, "GET /opds", db.RoleReader, s.handleOPDSRoot)
	s.route(mux, "GET /opds/{$}", db.RoleReader, s.handleOPDSRoot)
	s.route(mux, "GET /opds/books", db.RoleReader, s.handleOPDSBooks)
	s.route(mux, "GET /opds/search", db.RoleReader, s.handleOPDSSearch)
	s.route(mux, "GET /opds/osd", db.RoleReader, s.handleOPDSOpenSearch)
	s.route(mux, "GET /opds/recent", db.RoleReader, s.handleOPDSRecent)
	s.route(mux, "GET /opds/shelves", db.RoleReader, s.handleOPDSShelves)
	s.route(mux, "GET /opds/shelves/{id}", db.RoleReader, s.handleOPDSShelf)
	s.route(mux, "GET /opds/series", db.RoleReader, s.handleOPDSSeries)
	s.route(mux, "GET /opds/tags", db.RoleReader, s.handleOPDSTags)

	// KOReader sync routes. The app-password token is part of the base URL:
	// /kosync/{token}/users/auth, then KOReader appends syncs/progress paths.
	s.route(mux, "GET /kosync/{token}/users/auth", db.RoleReader, s.handleKOReaderAuth)
	s.route(mux, "PUT /kosync/{token}/syncs/progress", db.RoleReader, s.handleKOReaderProgressSave)
	s.route(mux, "GET /kosync/{token}/syncs/progress/{document}", db.RoleReader, s.handleKOReaderProgress)

	// Experimental native Kobo library sync. This uses a dedicated connection
	// URL, not a general app password: the connection owns one selected shelf and
	// every metadata/content lookup is narrowed to its last reconciled projection
	// as well as the account's current content scope.
	s.route(mux, "GET /kobo/{token}", db.RoleReader, s.handleKoboRoot)
	s.route(mux, "GET /kobo/{token}/{$}", db.RoleReader, s.handleKoboRoot)
	s.route(mux, "GET /kobo/{token}/v1/initialization", db.RoleReader, s.handleKoboInitialization)
	s.route(mux, "POST /kobo/{token}/v1/auth/device", db.RoleReader, s.handleKoboAuth)
	s.route(mux, "POST /kobo/{token}/v1/auth/refresh", db.RoleReader, s.handleKoboAuth)
	s.route(mux, "GET /kobo/{token}/v1/library/sync", db.RoleReader, s.handleKoboLibrarySync)
	s.route(mux, "GET /kobo/{token}/v1/library/{id}/metadata", db.RoleReader, s.handleKoboMetadata)
	s.route(mux, "GET /kobo/{token}/{id}/{width}/{height}/{greyscale}/image.jpg", db.RoleReader, s.handleKoboCover)
	s.route(mux, "GET /kobo/{token}/{id}/{width}/{height}/{quality}/{greyscale}/image.jpg", db.RoleReader, s.handleKoboCover)
	s.route(mux, "GET /kobo/{token}/download/{id}/{format}", db.RoleReader, s.handleKoboDownload)

	// API routes
	s.route(mux, "GET /api/me", db.RoleReader, s.handleAPIMe)
	s.route(mux, "GET /api/settings", db.RoleReader, s.handleAPISettings)
	s.route(mux, "PUT /api/settings", db.RoleReader, s.handleAPISettingsSave)
	s.route(mux, "GET /api/admin/storage", db.RoleAdmin, s.handleAPIAdminStorage)
	s.route(mux, "PATCH /api/admin/storage", db.RoleAdmin, s.handleAPIAdminStorageSave)
	s.route(mux, "POST /api/admin/storage/scan", db.RoleAdmin, s.handleAPIAdminStorageScan)
	s.route(mux, "POST /api/admin/storage/import/preview", db.RoleAdmin, s.handleAPIAdminStorageImportPreview)
	s.route(mux, "POST /api/admin/storage/import", db.RoleAdmin, s.handleAPIAdminStorageImportRun)
	s.route(mux, "POST /api/admin/storage/writeback/retry", db.RoleAdmin, s.handleAPIAdminWritebackRetry)
	s.route(mux, "PUT /api/admin/delivery", db.RoleAdmin, s.handleAPIAdminDeliverySave)
	s.route(mux, "GET /api/admin/email", db.RoleAdmin, s.handleAPIAdminEmail)
	s.route(mux, "PUT /api/admin/email", db.RoleAdmin, s.handleAPIAdminEmailSave)
	s.route(mux, "POST /api/admin/email/test", db.RoleAdmin, s.handleAPIAdminEmailTest)
	s.route(mux, "GET /api/app-tokens", db.RoleReader, s.handleAPIAppTokens)
	s.route(mux, "POST /api/app-tokens", db.RoleReader, s.handleAPIAppTokenCreate)
	s.route(mux, "DELETE /api/app-tokens/{id}", db.RoleReader, s.handleAPIAppTokenDelete)
	s.route(mux, "GET /api/kobo-connection", db.RoleReader, s.handleAPIKoboConnection)
	s.route(mux, "POST /api/kobo-connection", db.RoleReader, s.handleAPIKoboConnectionCreate)
	s.route(mux, "DELETE /api/kobo-connection", db.RoleReader, s.handleAPIKoboConnectionDelete)
	s.route(mux, "GET /api/users", db.RoleAdmin, s.handleAPIUsers)
	s.route(mux, "POST /api/users", db.RoleAdmin, s.handleAPIUserCreate)
	s.route(mux, "PATCH /api/users/{id}", db.RoleAdmin, s.handleAPIUserUpdate)
	s.route(mux, "DELETE /api/users/{id}", db.RoleAdmin, s.handleAPIUserDelete)
	// Readers may change their own password; the handler enforces self-or-admin
	// after the minimum-role gate resolves both the caller and target id.
	s.route(mux, "POST /api/users/{id}/password", db.RoleReader, s.handleAPIUserPassword)
	s.route(mux, "POST /api/import", db.RoleMember, s.handleAPIImport)
	s.route(mux, "POST /api/search/validate", db.RoleReader, s.handleAPISearchValidate)
	s.route(mux, "GET /api/books", db.RoleReader, s.handleAPIBooks)
	s.route(mux, "GET /api/books/jumps", db.RoleReader, s.handleAPIBookJumps)
	s.route(mux, "GET /api/devices", db.RoleReader, s.handleAPIDeliveryDevices)
	s.route(mux, "POST /api/devices", db.RoleReader, s.handleAPIDeliveryDeviceCreate)
	s.route(mux, "PATCH /api/devices/{id}", db.RoleReader, s.handleAPIDeliveryDeviceUpdate)
	s.route(mux, "DELETE /api/devices/{id}", db.RoleReader, s.handleAPIDeliveryDeviceDelete)
	s.route(mux, "GET /api/send/options", db.RoleReader, s.handleAPISendOptions)
	s.route(mux, "POST /api/deliveries", db.RoleReader, s.handleAPIDeliveryCreate)
	s.route(mux, "GET /api/deliveries", db.RoleReader, s.handleAPIDeliveries)
	s.route(mux, "GET /api/deliveries/{id}", db.RoleReader, s.handleAPIDelivery)
	s.route(mux, "GET /api/books/{id}/sequence", db.RoleReader, s.handleAPIBookSequence)
	s.route(mux, "GET /api/books/{id}", db.RoleReader, s.handleAPIBookDetail)
	s.route(mux, "PUT /api/books/{id}/reading-status", db.RoleReader, s.handleAPIReadingStatusSave)
	s.route(mux, "POST /api/books/{id}/reading-status/undo", db.RoleReader, s.handleAPIReadingStatusUndo)
	s.route(mux, "GET /api/books/{id}/metadata-candidates", db.RoleMember, s.handleAPIMetadataCandidates)
	s.route(mux, "POST /api/books/{id}/writeback", db.RoleAdmin, s.handleAPIBookWriteback)
	s.route(mux, "GET /api/metadata/description", db.RoleMember, s.handleAPIMetadataDescription)
	s.route(mux, "GET /api/books/{id}/shelves", db.RoleReader, s.handleAPIBookShelves)
	// The literal /bulk segment is more specific than /{id}, so Go's mux routes
	// it here rather than to the single-book edit handler above.
	s.route(mux, "PATCH /api/books/bulk", db.RoleMember, s.handleAPIBulkEdit)
	s.route(mux, "POST /api/books/bulk/writeback", db.RoleAdmin, s.handleAPIBulkWriteback)
	s.route(mux, "POST /api/books/bulk/trash", db.RoleMember, s.handleAPIBulkTrash)
	s.route(mux, "PATCH /api/books/{id}", db.RoleMember, s.handleAPIBookEdit)
	s.route(mux, "DELETE /api/books/{id}", db.RoleMember, s.handleAPIBookDelete)
	s.route(mux, "POST /api/books/{id}/restore", db.RoleMember, s.handleAPIBookRestore)
	s.route(mux, "DELETE /api/books/{id}/purge", db.RoleAdmin, s.handleAPIBookPurge)
	s.route(mux, "GET /api/trash", db.RoleMember, s.handleAPITrash)
	s.route(mux, "DELETE /api/trash", db.RoleAdmin, s.handleAPITrashEmpty)
	s.route(mux, "GET /api/cleanup", db.RoleMember, s.handleAPICleanup)
	s.route(mux, "POST /api/cleanup/duplicates/merge", db.RoleMember, s.handleAPICleanupDuplicateMerge)
	s.route(mux, "POST /api/cleanup/duplicates/dismiss", db.RoleMember, s.handleAPICleanupDuplicateDismiss)
	s.route(mux, "POST /api/books/{id}/cover-generated-preview", db.RoleMember, s.handleAPIGeneratedCoverPreview)
	s.route(mux, "GET /api/books/{id}/cover-search", db.RoleMember, s.handleAPICoverSearch)
	s.route(mux, "GET /api/books/{id}/cover-search/preview", db.RoleMember, s.handleAPICoverSearchPreview)
	s.route(mux, "POST /api/books/{id}/cover-search", db.RoleMember, s.handleAPICoverSearchApply)
	s.route(mux, "POST /api/books/{id}/cover", db.RoleMember, s.handleAPICoverUpload)
	s.route(mux, "POST /api/books/{id}/cover-url", db.RoleMember, s.handleAPICoverURL)
	s.route(mux, "GET /api/reader/continue", db.RoleReader, s.handleAPIContinueReading)
	s.route(mux, "GET /api/reader/preferences", db.RoleReader, s.handleAPIReaderPreferences)
	s.route(mux, "PUT /api/reader/preferences", db.RoleReader, s.handleAPIReaderPreferencesSave)
	s.route(mux, "GET /api/reader/assets/{id}/state", db.RoleReader, s.handleAPIReaderState)
	s.route(mux, "PUT /api/reader/assets/{id}/state", db.RoleReader, s.handleAPIReaderStateSave)
	s.route(mux, "DELETE /api/reader/assets/{id}/state", db.RoleReader, s.handleAPIReaderStateReset)
	s.route(mux, "POST /api/reader/assets/{id}/touch", db.RoleReader, s.handleAPIReaderStateTouch)
	s.route(mux, "GET /api/reader/assets/{id}/annotations", db.RoleReader, s.handleAPIAnnotations)
	s.route(mux, "GET /api/reader/assets/{id}/annotations/export", db.RoleReader, s.handleAPIAnnotationExport)
	s.route(mux, "POST /api/reader/assets/{id}/annotations", db.RoleReader, s.handleAPIAnnotationCreate)
	s.route(mux, "PATCH /api/reader/assets/{id}/annotations/{annotationID}", db.RoleReader, s.handleAPIAnnotationUpdate)
	s.route(mux, "DELETE /api/reader/assets/{id}/annotations/{annotationID}", db.RoleReader, s.handleAPIAnnotationDelete)
	s.route(mux, "GET /api/shelves", db.RoleReader, s.handleAPIShelves)
	s.route(mux, "POST /api/shelves", db.RoleReader, s.handleAPIShelfCreate)
	s.route(mux, "PATCH /api/shelves/{id}", db.RoleReader, s.handleAPIShelfUpdate)
	s.route(mux, "DELETE /api/shelves/{id}", db.RoleReader, s.handleAPIShelfDelete)
	s.route(mux, "POST /api/shelves/{id}/books/bulk", db.RoleReader, s.handleAPIShelfBulkBooks)
	s.route(mux, "PUT /api/shelves/{id}/books/{workID}", db.RoleReader, s.handleAPIShelfAddBook)
	s.route(mux, "DELETE /api/shelves/{id}/books/{workID}", db.RoleReader, s.handleAPIShelfRemoveBook)
	s.route(mux, "GET /api/series", db.RoleReader, s.handleAPISeries)
	s.route(mux, "GET /api/authors", db.RoleReader, s.handleAPIAuthors)
	s.route(mux, "GET /api/authors/info", db.RoleMember, s.handleAPIAuthorInfo)
	s.route(mux, "GET /api/authors/list", db.RoleMember, s.handleAPIAuthorList)
	s.route(mux, "POST /api/authors/rename", db.RoleMember, s.handleAPIAuthorRename)
	s.route(mux, "POST /api/authors/sort-name", db.RoleMember, s.handleAPIAuthorSortName)
	s.route(mux, "GET /api/tags", db.RoleReader, s.handleAPITags)

	// Download & Covers routes
	s.route(mux, "GET /download/{id}", db.RoleReader, s.handleDownload)
	s.route(mux, "GET /download/{id}/as/{target}", db.RoleReader, s.handleDownloadAs)
	s.route(mux, "GET /read/assets/{id}", db.RoleReader, s.handleReadAsset)
	s.route(mux, "GET /covers/{id}", db.RoleReader, s.handleCover)

	// Static assets. staticFS embeds files under "static/", so serve from the
	// "static" subtree — otherwise StripPrefix("/static/") would look for files
	// at the FS root and 404.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static assets: %w", err)
	}
	staticHandler := http.StripPrefix("/static/", newStaticAssets(staticSub))
	s.route(mux, "GET /static/", rolePublic, staticHandler.ServeHTTP)

	return mux, nil
}

const rolePublic = "public"

// route registers a handler with its minimum role on the registration line,
// making routes() the single audit surface for that authorization rule. A route
// cannot be added without declaring access, so fail-open-by-omission is a
// compile error rather than a discipline failure (which is why there is no
// role middleware and no separate path→role table to drift). rolePublic marks
// the intentionally open routes (login, setup, static). Credential types and
// unauthenticated responses are family-level rules in authMiddleware. Two kinds
// of authorization stay in handlers, after this gate: visibility
// (CanAccessWork/Asset needs the resolved id and answers 404), and rules that
// are not a minimum role ("admin or self").
func (s *Server) route(mux *http.ServeMux, pattern, minRole string, h http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if minRole != rolePublic {
			user, ok := s.requireRole(w, r, minRole)
			if !ok {
				return
			}
			r = r.WithContext(withUser(r.Context(), user))
		}
		h(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The auth entry points and static assets must be reachable before login.
		path := r.URL.Path
		if path == "/login" || path == "/setup" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		// Credential-bearing device paths authenticate through their own URL token,
		// even when a browser happens to send a Polka session cookie. Kobo handlers
		// also need the exact connection id to enforce its selected-shelf boundary.
		if koboPath(path) {
			connection, ok, err := s.db.KoboConnectionByToken(koboTokenFromPath(path))
			if err != nil {
				serverError(w, err)
				return
			}
			if ok {
				ctx := withUserID(r.Context(), connection.UserID)
				ctx = withKoboConnectionID(ctx, connection.ID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if kosyncPath(path) {
			uid, ok, err := s.kosyncTokenUserID(r)
			if err != nil {
				serverError(w, err)
				return
			}
			if ok {
				next.ServeHTTP(w, r.WithContext(withUserID(r.Context(), uid)))
				return
			}
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			uid, ok, err := s.sessions.lookup(cookie.Value)
			if err != nil {
				serverError(w, err)
				return
			}
			if ok {
				next.ServeHTTP(w, r.WithContext(withUserID(r.Context(), uid)))
				return
			}
		}

		if basicAuthPath(path) {
			uid, ok, err := s.basicAuthUserID(r)
			if errors.Is(err, errPasswordAuthBusy) {
				writePasswordAuthBusy(w)
				return
			}
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				serverError(w, err)
				return
			}
			if ok {
				next.ServeHTTP(w, r.WithContext(withUserID(r.Context(), uid)))
				return
			}
			challengeBasicAuth(w)
			return
		}

		// Unauthenticated. Programmatic clients get a 401; humans are sent to the
		// right place — first-run setup when no account exists yet, else login.
		if programmaticAuthPath(path) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if n, err := s.db.CountUsers(); err == nil && n == 0 {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

func (s *Server) basicAuthUserID(r *http.Request) (string, bool, error) {
	username, password, ok := r.BasicAuth()
	if !ok {
		return "", false, nil
	}
	// App tokens are self-identifying app passwords. Try the cheap token hash
	// lookup before bcrypt so OPDS/download/cover clients do not pay password
	// verification on every request.
	if uid, ok, err := s.db.AppTokenUserID(password); err != nil || ok {
		return uid, ok, err
	}

	user, err := s.authenticatePassword(r.Context(), username, password)
	if err != nil {
		return "", false, err
	}
	if user != nil {
		return user.ID, true, nil
	}
	return "", false, nil
}

const (
	maxConcurrentPasswordAuth = 2
	passwordAuthWaitTimeout   = 5 * time.Second
)

var errPasswordAuthBusy = errors.New("password authentication is busy")

func (s *Server) authenticatePassword(ctx context.Context, username, password string) (*db.User, error) {
	slots := s.passwordAuthGate()
	timer := time.NewTimer(passwordAuthWaitTimeout)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-timer.C:
		return nil, errPasswordAuthBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.db.Authenticate(username, password)
}

func (s *Server) passwordAuthGate() chan struct{} {
	s.passwordAuthOnce.Do(func() {
		s.passwordAuthSlots = make(chan struct{}, maxConcurrentPasswordAuth)
	})
	return s.passwordAuthSlots
}

func writePasswordAuthBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "Too many authentication attempts", http.StatusTooManyRequests)
}

func (s *Server) kosyncTokenUserID(r *http.Request) (string, bool, error) {
	return s.db.AppTokenUserID(kosyncTokenFromPath(r.URL.Path))
}

func basicAuthPath(path string) bool {
	return opdsPath(path) || strings.HasPrefix(path, "/download/") || strings.HasPrefix(path, "/covers/")
}

func programmaticAuthPath(path string) bool {
	return strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/download/") ||
		strings.HasPrefix(path, "/read/assets/") ||
		strings.HasPrefix(path, "/covers/") ||
		koboPath(path) ||
		kosyncPath(path) ||
		opdsPath(path)
}

func opdsPath(path string) bool {
	return path == "/opds" || strings.HasPrefix(path, "/opds/")
}

func kosyncPath(path string) bool {
	return strings.HasPrefix(path, "/kosync/")
}

func koboPath(path string) bool {
	return strings.HasPrefix(path, "/kobo/")
}

func koboTokenFromPath(path string) string {
	rest := strings.TrimPrefix(path, "/kobo/")
	if rest == path {
		return ""
	}
	if before, _, ok := strings.Cut(rest, "/"); ok {
		return before
	}
	return rest
}

func kosyncTokenFromPath(path string) string {
	rest := strings.TrimPrefix(path, "/kosync/")
	if rest == path {
		return ""
	}
	if before, _, ok := strings.Cut(rest, "/"); ok {
		return before
	}
	return ""
}

func challengeBasicAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="polka"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

// bootstrapAdmin seeds the first admin account from env/flag credentials when
// the library has no users yet. With no credentials supplied it does nothing and
// the first-run setup page takes over; once any user exists it is a no-op (the
// credentials are ignored, so leaving them in a unit file is harmless). When the
// library is empty and no bootstrap creds are given, it logs how to proceed.
func bootstrapAdmin(database *db.DB, adminUser, adminPassword string) error {
	n, err := database.CountUsers()
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return nil
	}
	if adminUser == "" || adminPassword == "" {
		log.Println("No users yet — open the web UI to create the first admin, or set POLKA_ADMIN_USER/POLKA_ADMIN_PASSWORD (or run `polka user add --admin`).")
		return nil
	}
	if _, err := database.CreateUser(adminUser, adminPassword, db.RoleAdmin); err != nil {
		return fmt.Errorf("bootstrap admin %q: %w", adminUser, err)
	}
	log.Printf("Created initial admin user %q.", adminUser)
	return nil
}
