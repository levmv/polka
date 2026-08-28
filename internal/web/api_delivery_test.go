package web

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/delivery"
	"github.com/levmv/polka/internal/workslot"
)

func TestAPIDeliveryDevicesLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleReader)
	bob := mustUser(t, database, "bob", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/devices", map[string]any{
		"name":  "Kindle",
		"email": "alice@kindle.com",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create device status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var kindle DeliveryDeviceDTO
	if err := json.UnmarshalRead(w.Body, &kindle); err != nil {
		t.Fatalf("decode kindle: %v", err)
	}
	if kindle.Preset != db.DeliveryPresetKindle || !kindle.IsDefault {
		t.Fatalf("created kindle = %+v, want kindle default", kindle)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/devices", map[string]any{
		"name":       "Pocket",
		"email":      "alice@pbsync.com",
		"is_default": true,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create second device status = %d; body: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/devices", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list alice devices status = %d; body: %s", w.Code, w.Body.String())
	}
	var aliceDevices []DeliveryDeviceDTO
	if err := json.UnmarshalRead(w.Body, &aliceDevices); err != nil {
		t.Fatalf("decode alice devices: %v", err)
	}
	if len(aliceDevices) != 2 || !aliceDevices[0].IsDefault {
		t.Fatalf("alice devices = %+v, want two with default first", aliceDevices)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/devices", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list bob devices status = %d; body: %s", w.Code, w.Body.String())
	}
	var bobDevices []DeliveryDeviceDTO
	if err := json.UnmarshalRead(w.Body, &bobDevices); err != nil {
		t.Fatalf("decode bob devices: %v", err)
	}
	if len(bobDevices) != 0 {
		t.Fatalf("devices leaked to bob: %+v", bobDevices)
	}
}

func TestAPIAdminEmailSaveRejectsInvalidNumericSettings(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "admin", db.RoleAdmin)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name:    "zero port",
			payload: map[string]any{"port": 0},
			want:    "SMTP port is invalid",
		},
		{
			name:    "negative attachment limit",
			payload: map[string]any{"attachment_limit_mb": -5},
			want:    "Attachment limit must be between 1 and 200 MB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPut, "/api/admin/email", tt.payload))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("save email settings status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("save email settings body = %q, want %q", w.Body.String(), tt.want)
			}
		})
	}
}

func TestAPISendOptionsPlansKindleEPUB(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "admin", db.RoleAdmin)
	if _, err := database.Exec("UPDATE assets SET format = 'epub', current_size = 1024, is_primary = 1 WHERE id = 'asset_1'"); err != nil {
		t.Fatalf("update asset: %v", err)
	}
	enableSending(t, database)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPut, "/api/admin/email", map[string]any{
		"host":                "smtp.example.org",
		"port":                587,
		"security":            "starttls",
		"from_address":        "books@example.org",
		"attachment_limit_mb": 25,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save email settings status = %d; body: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/devices", map[string]any{
		"name":  "Kindle",
		"email": "admin@kindle.com",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create device status = %d; body: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodGet, "/api/send/options?work=w_1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("send options status = %d; body: %s", w.Code, w.Body.String())
	}
	var options SendOptionsDTO
	if err := json.UnmarshalRead(w.Body, &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if !options.Configured || len(options.Devices) != 1 || options.Devices[0].Plan == nil {
		t.Fatalf("options = %+v, want one sendable plan", options)
	}
	if options.Devices[0].Plan.AssetID != "asset_1" || options.Devices[0].Plan.Format != "epub" {
		t.Fatalf("plan = %+v, want asset_1 epub", options.Devices[0].Plan)
	}
	if len(options.Devices[0].Choices) != 1 || options.Devices[0].Choices[0].AssetID != "asset_1" {
		t.Fatalf("choices = %+v, want server-provided asset_1 choice", options.Devices[0].Choices)
	}
}

func TestAPISendOptionsChoicesUsePersistedFormat(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "admin", db.RoleAdmin)
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w_fb2_zip', 'FB2 Zip', 'FB2 Zip');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, current_size, is_primary)
			VALUES ('asset_fb2_zip', 'w_fb2_zip', 'Books/fb2.zip', 'fb2.zip', '.fb2.zip', 'fb2', 1024, 1);
	`); err != nil {
		t.Fatalf("seed fb2.zip asset: %v", err)
	}
	enableSending(t, database)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPut, "/api/admin/email", map[string]any{
		"host":                "smtp.example.org",
		"port":                587,
		"security":            "starttls",
		"from_address":        "books@example.org",
		"attachment_limit_mb": 25,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save email settings status = %d; body: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/devices", map[string]any{
		"name":   "Generic",
		"email":  "reader@example.org",
		"preset": "generic",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create generic device status = %d; body: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodGet, "/api/send/options?work=w_fb2_zip", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("send options status = %d; body: %s", w.Code, w.Body.String())
	}
	var options SendOptionsDTO
	if err := json.UnmarshalRead(w.Body, &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if len(options.Devices) != 1 {
		t.Fatalf("devices = %+v, want one device", options.Devices)
	}
	if options.Devices[0].Plan != nil {
		t.Fatalf("generic auto plan = %+v, want explicit choice only", options.Devices[0].Plan)
	}
	if len(options.Devices[0].Choices) < 1 {
		t.Fatalf("choices = %+v, want FB2 choice", options.Devices[0].Choices)
	}
	choice := options.Devices[0].Choices[0]
	if choice.AssetID != "asset_fb2_zip" || choice.Format != "fb2" || choice.Target != "" {
		t.Fatalf("choice = %+v, want native FB2 from persisted format", choice)
	}
}

func TestPrepareDeliveryCopyCopiesNativeAssetToTemp(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{db: database, dataDir: dir}
	job := db.DeliveryJob{
		ID:       "dj_native_copy",
		AssetID:  sql.NullString{String: "asset_1", Valid: true},
		Filename: "The Hobbit.epub",
	}

	copy, cleanup, err := s.prepareDeliveryCopy(context.Background(), job)
	if err != nil {
		t.Fatalf("prepareDeliveryCopy: %v", err)
	}
	original := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
	if copy.Path == original {
		cleanup()
		t.Fatalf("native delivery copy path = original path %q", copy.Path)
	}
	if gotDir, wantDir := filepath.Dir(copy.Path), filepath.Join(dir, "tmp", "delivery"); gotDir != wantDir {
		cleanup()
		t.Fatalf("delivery copy dir = %q, want %q", gotDir, wantDir)
	}
	if copy.Filename != "The Hobbit.epub" || copy.MediaType != "application/epub+zip" || copy.Size != int64(len("epub content")) {
		cleanup()
		t.Fatalf("delivery copy metadata = %+v", copy)
	}
	got, err := os.ReadFile(copy.Path)
	if err != nil {
		cleanup()
		t.Fatalf("read delivery copy: %v", err)
	}
	if string(got) != "epub content" {
		cleanup()
		t.Fatalf("delivery copy content = %q", got)
	}
	cleanup()
	if _, err := os.Stat(copy.Path); !os.IsNotExist(err) {
		t.Fatalf("delivery cleanup stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original asset after cleanup: %v", err)
	}
}

func TestPrepareDeliveryCopyWaitsForStorageMutationBeforeReportingMissing(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	queue := workslot.New()
	s := &Server{db: database, dataDir: dir, storageQueue: queue}
	job := db.DeliveryJob{
		ID:       "dj_missing_copy",
		AssetID:  sql.NullString{String: "asset_1", Valid: true},
		Filename: "The Hobbit.epub",
	}
	if err := os.Remove(filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")); err != nil {
		t.Fatalf("remove asset: %v", err)
	}

	release, err := queue.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire storage slot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := s.prepareDeliveryCopy(ctx, job); !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("prepare while storage mutation is active = %v; want context deadline", err)
	}
	release()

	_, _, err = s.prepareDeliveryCopy(context.Background(), job)
	userErr, ok := errors.AsType[deliveryUserError](err)
	if !ok || userErr.UserMessage() != deliveryMessageFileMissing {
		t.Fatalf("prepare after storage mutation = %v; want terminal missing-file error", err)
	}
}

func TestRunDeliveryJobUsesTransportAndMarksSent(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	seedDeliveryEmailSettings(t, database, 25)

	user := mustUser(t, database, "sender", db.RoleReader)
	job := createQueuedDeliveryJob(t, database, user.ID, sql.NullString{})

	var copyPath string
	transport := &stubDeliveryTransport{
		send: func(ctx context.Context, copy delivery.DeliveryCopy, profile delivery.SMTPProfile) error {
			copyPath = copy.Path
			if profile.To != "reader@kindle.com" || profile.Subject != "The Hobbit" {
				t.Fatalf("profile = %+v, want reader subject", profile)
			}
			if profile.Config.Host != "smtp.example.org" {
				t.Fatalf("SMTP host = %q, want smtp.example.org", profile.Config.Host)
			}
			got, err := os.ReadFile(copy.Path)
			if err != nil {
				t.Fatalf("read delivery copy during transport: %v", err)
			}
			if string(got) != "epub content" {
				t.Fatalf("delivery copy content = %q", got)
			}
			current, err := database.GetDeliveryJobByID(job.ID)
			if err != nil {
				t.Fatalf("get job during transport: %v", err)
			}
			if current.Status != db.DeliveryStatusSending {
				t.Fatalf("status during transport = %q, want sending", current.Status)
			}
			return nil
		},
	}
	s := &Server{db: database, dataDir: dir, deliveryTransport: transport}

	if err := s.runDeliveryJob(context.Background(), job.ID); err != nil {
		t.Fatalf("runDeliveryJob: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.calls)
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("delivery temp copy after success stat err = %v, want not exist", err)
	}
	reloaded, err := database.GetDeliveryJobByID(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if reloaded.Status != db.DeliveryStatusSent || reloaded.Error != "" || !reloaded.SentAt.Valid {
		t.Fatalf("job after success = %+v, want sent with sent_at", reloaded)
	}
	if !reloaded.SizeBytes.Valid || reloaded.SizeBytes.Int64 != int64(len("epub content")) {
		t.Fatalf("job size = %+v, want %d", reloaded.SizeBytes, len("epub content"))
	}
}

func TestRunDeliveryJobHidesUnexpectedTransportErrorAndCleansTempCopy(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	seedDeliveryEmailSettings(t, database, 25)

	user := mustUser(t, database, "sender", db.RoleReader)
	job := createQueuedDeliveryJob(t, database, user.ID, sql.NullString{})

	var copyPath string
	transport := &stubDeliveryTransport{
		send: func(ctx context.Context, copy delivery.DeliveryCopy, profile delivery.SMTPProfile) error {
			copyPath = copy.Path
			if _, err := os.Stat(copy.Path); err != nil {
				t.Fatalf("delivery copy missing during failing transport: %v", err)
			}
			return fmt.Errorf("transport failed")
		},
	}
	s := &Server{db: database, dataDir: dir, deliveryTransport: transport}

	if err := s.runDeliveryJob(context.Background(), job.ID); err != nil {
		t.Fatalf("runDeliveryJob: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.calls)
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("delivery temp copy after failure stat err = %v, want not exist", err)
	}
	reloaded, err := database.GetDeliveryJobByID(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if reloaded.Status != db.DeliveryStatusFailed || reloaded.Error != deliveryMessageFailed || reloaded.SentAt.Valid {
		t.Fatalf("job after failure = %+v, want generic user-safe error", reloaded)
	}
}

func TestRunDeliveryJobStoresTransportUserMessage(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	seedDeliveryEmailSettings(t, database, 25)

	user := mustUser(t, database, "sender", db.RoleReader)
	job := createQueuedDeliveryJob(t, database, user.ID, sql.NullString{})
	transport := &stubDeliveryTransport{
		send: func(ctx context.Context, copy delivery.DeliveryCopy, profile delivery.SMTPProfile) error {
			return testUserMessageError{message: "SMTP authentication failed", detail: "535 bad credentials"}
		},
	}
	s := &Server{db: database, dataDir: dir, deliveryTransport: transport}

	if err := s.runDeliveryJob(context.Background(), job.ID); err != nil {
		t.Fatalf("runDeliveryJob: %v", err)
	}
	reloaded, err := database.GetDeliveryJobByID(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if reloaded.Status != db.DeliveryStatusFailed || reloaded.Error != "SMTP authentication failed" {
		t.Fatalf("job after failure = %+v, want mapped user message", reloaded)
	}
}

func TestRunDeliveryJobFinalSizeGuardSkipsTransport(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	seedDeliveryEmailSettings(t, database, 1)

	assetPath := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
	if err := os.WriteFile(assetPath, make([]byte, 2*1024*1024), 0o644); err != nil {
		t.Fatalf("write large asset: %v", err)
	}
	user := mustUser(t, database, "sender", db.RoleReader)
	job := createQueuedDeliveryJob(t, database, user.ID, sql.NullString{})
	transport := &stubDeliveryTransport{}
	s := &Server{db: database, dataDir: dir, deliveryTransport: transport}

	if err := s.runDeliveryJob(context.Background(), job.ID); err != nil {
		t.Fatalf("runDeliveryJob: %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.calls)
	}
	reloaded, err := database.GetDeliveryJobByID(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if reloaded.Status != db.DeliveryStatusFailed {
		t.Fatalf("job status = %q, want failed", reloaded.Status)
	}
	if !strings.Contains(reloaded.Error, "File is too large for email delivery") {
		t.Fatalf("job error = %q, want size guard", reloaded.Error)
	}
}

func TestDeliveryWorkerDrainsDurableQueueSerially(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	seedDeliveryEmailSettings(t, database, 25)

	user := mustUser(t, database, "worker", db.RoleReader)
	jobs := []*db.DeliveryJob{
		createQueuedDeliveryJob(t, database, user.ID, sql.NullString{}),
		createQueuedDeliveryJob(t, database, user.ID, sql.NullString{}),
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	transport := &stubDeliveryTransport{send: func(context.Context, delivery.DeliveryCopy, delivery.SMTPProfile) error {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	}}
	s := &Server{
		db:                database,
		dataDir:           dir,
		deliveryWake:      make(chan struct{}, 1),
		deliveryTransport: transport,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.runDeliveryWorker(ctx)
		close(done)
	}()
	<-firstStarted

	statusCounts := map[string]int{}
	for _, job := range jobs {
		current, err := database.GetDeliveryJobByID(job.ID)
		if err != nil {
			t.Fatalf("get job while first send is blocked: %v", err)
		}
		statusCounts[current.Status]++
	}
	if statusCounts[db.DeliveryStatusSending] != 1 || statusCounts[db.DeliveryStatusQueued] != 1 {
		t.Fatalf("statuses while first send is blocked = %v, want one sending and one queued", statusCounts)
	}

	close(releaseFirst)
	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := 0
		for _, job := range jobs {
			current, err := database.GetDeliveryJobByID(job.ID)
			if err != nil {
				t.Fatalf("get drained job: %v", err)
			}
			if current.Status == db.DeliveryStatusSent {
				sent++
			}
		}
		if sent == len(jobs) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not drain both jobs; transport calls = %d", calls.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery worker did not stop after cancellation")
	}
	if calls.Load() != 2 {
		t.Fatalf("transport calls = %d, want 2", calls.Load())
	}
}

// Sending is off until an admin turns it on, and turning it off has to stop
// sends rather than only hide the button: an admin switches it off exactly when
// a configured transport must no longer be used.
func TestSendingSwitchGatesDeliveryAPI(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "admin", db.RoleAdmin)
	seedDeliveryEmailSettings(t, database, 25)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/devices", map[string]any{
		"name":  "Kindle",
		"email": "admin@kindle.com",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create device status = %d; body: %s", w.Code, w.Body.String())
	}

	sendOptions := func() SendOptionsDTO {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, jsonRequest(t, s, admin.ID, http.MethodGet, "/api/send/options?work=w_1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("send options status = %d; body: %s", rec.Code, rec.Body.String())
		}
		var options SendOptionsDTO
		if err := json.UnmarshalRead(rec.Body, &options); err != nil {
			t.Fatalf("decode options: %v", err)
		}
		return options
	}

	if options := sendOptions(); options.Configured || len(options.Devices) != 0 {
		t.Fatalf("options while off = %+v, want no configured devices", options)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/deliveries", map[string]any{
		"work_id": "w_1",
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("create delivery while off = %d, want %d; body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPut, "/api/admin/delivery", map[string]any{
		"enabled": true,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("enable sending status = %d; body: %s", w.Code, w.Body.String())
	}
	if options := sendOptions(); !options.Configured || len(options.Devices) != 1 {
		t.Fatalf("options after enabling = %+v, want one configured device", options)
	}
}

func TestAPIAdminEmailRequiresAdmin(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	reader := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodGet, "/api/admin/email", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("reader admin email status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

type stubDeliveryTransport struct {
	calls int
	send  func(context.Context, delivery.DeliveryCopy, delivery.SMTPProfile) error
}

func (t *stubDeliveryTransport) Send(ctx context.Context, copy delivery.DeliveryCopy, profile delivery.SMTPProfile) error {
	t.calls++
	if t.send == nil {
		return nil
	}
	return t.send(ctx, copy, profile)
}

type testUserMessageError struct {
	message string
	detail  string
}

func (e testUserMessageError) Error() string {
	return e.message + ": " + e.detail
}

func (e testUserMessageError) UserMessage() string {
	return e.message
}

func enableSending(t *testing.T, database *db.DB) {
	t.Helper()
	if err := delivery.SaveEnabled(database, true); err != nil {
		t.Fatalf("enable sending: %v", err)
	}
}

func seedDeliveryEmailSettings(t *testing.T, database *db.DB, attachmentLimitMB int) {
	t.Helper()
	if err := delivery.SaveSMTPConfig(database, delivery.SMTPConfig{
		Host:              "smtp.example.org",
		Port:              delivery.DefaultSMTPPort,
		Security:          delivery.SMTPSecurityPlain,
		FromAddress:       "books@example.org",
		FromName:          "polka",
		AttachmentLimitMB: attachmentLimitMB,
	}); err != nil {
		t.Fatalf("save email settings: %v", err)
	}
}

func createQueuedDeliveryJob(t *testing.T, database *db.DB, userID string, target sql.NullString) *db.DeliveryJob {
	t.Helper()
	job, err := database.CreateDeliveryJob(db.DeliveryJob{
		UserID:      userID,
		DeviceName:  "Kindle",
		DeviceEmail: "reader@kindle.com",
		Preset:      db.DeliveryPresetKindle,
		WorkID:      "w_1",
		AssetID:     sql.NullString{String: "asset_1", Valid: true},
		Title:       "The Hobbit",
		Target:      target,
		Filename:    "The Hobbit.epub",
	})
	if err != nil {
		t.Fatalf("create delivery job: %v", err)
	}
	return job
}
