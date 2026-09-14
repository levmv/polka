package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestDeliveryDeviceLifecycleKeepsOneDefault(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "alice", RoleReader)

	first, err := database.CreateDeliveryDevice(context.Background(), user.ID, "Kindle", "alice@kindle.com", DeliveryPresetKindle, false)
	if err != nil {
		t.Fatalf("create first device: %v", err)
	}
	if !first.IsDefault {
		t.Fatalf("first device should become default: %+v", first)
	}
	second, err := database.CreateDeliveryDevice(context.Background(), user.ID, "PocketBook", "alice@pbsync.com", DeliveryPresetPocketBook, true)
	if err != nil {
		t.Fatalf("create second device: %v", err)
	}
	if !second.IsDefault {
		t.Fatalf("second device should be default: %+v", second)
	}
	first, err = GetDeliveryDevice(database.Read(t.Context()), user.ID, first.ID)
	if err != nil {
		t.Fatalf("reload first: %v", err)
	}
	if first.IsDefault {
		t.Fatalf("old default was not cleared: %+v", first)
	}

	if _, err := database.CreateDeliveryDevice(context.Background(), user.ID, "PocketBook", "other@pbsync.com", DeliveryPresetPocketBook, false); !errors.Is(err, ErrDeliveryDeviceNameExists) {
		t.Fatalf("duplicate name err = %v, want ErrDeliveryDeviceNameExists", err)
	}

	if err := database.DeleteDeliveryDevice(context.Background(), user.ID, second.ID); err != nil {
		t.Fatalf("delete second: %v", err)
	}
	first, err = GetDeliveryDevice(database.Read(t.Context()), user.ID, first.ID)
	if err != nil {
		t.Fatalf("reload promoted first: %v", err)
	}
	if !first.IsDefault {
		t.Fatalf("remaining device should be promoted: %+v", first)
	}
}

func TestDeliveryBookForPlanAppliesScope(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "reader", RoleReader)
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Allowed", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (11, 'Allowed', 'Allowed');
		INSERT INTO books (id, title, sort_title) VALUES (12, 'Blocked', 'Blocked');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, current_size, is_primary, original_sha256, current_sha256)
			VALUES (1, 11, 'a.epub', 'a.epub', '.epub', 'epub', 100, 1, randomblob(32), randomblob(32));
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, current_size, is_primary, original_sha256, current_sha256)
			VALUES (2, 12, 'b.epub', 'b.epub', '.epub', 'epub', 100, 1, randomblob(32), randomblob(32));
	`)

	if err := database.AddBookToShelf(t.Context(), shelf.ID, 0, 11); err != nil {
		t.Fatalf("add allowed to shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}

	book, assets, err := DeliveryBookForPlan(database.Read(t.Context()), scope, 11)
	if err != nil {
		t.Fatalf("allowed book: %v", err)
	}
	if book.ID != 11 || len(assets) != 1 || assets[0].ID != 1 {
		t.Fatalf("allowed book/assets = %+v %+v", book, assets)
	}
	if _, _, err := DeliveryBookForPlan(database.Read(t.Context()), scope, 12); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("blocked err = %v, want sql.ErrNoRows", err)
	}
}

func TestDeliveryJobLifecycle(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "alice", RoleReader)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256)
			VALUES (1, 1, 'a.epub', 'a.epub', '.epub', 'epub', randomblob(32), randomblob(32));
	`)

	device, err := database.CreateDeliveryDevice(context.Background(), user.ID, "Kindle", "alice@kindle.com", DeliveryPresetKindle, true)
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	job, err := database.CreateDeliveryJob(t.Context(), DeliveryJob{
		UserID:      user.ID,
		DeviceID:    sql.NullInt64{Int64: device.ID, Valid: true},
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		AssetID:     sql.NullInt64{Int64: 1, Valid: true},
		Title:       "Book",
		Filename:    "Book.epub",
		SizeBytes:   sql.NullInt64{Int64: 100, Valid: true},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.Status != DeliveryStatusQueued {
		t.Fatalf("new job status = %q", job.Status)
	}
	if err := database.SetDeliveryJobStatus(t.Context(), job.ID, DeliveryStatusSending, ""); err != nil {
		t.Fatalf("set sending: %v", err)
	}
	converting, err := database.CreateDeliveryJob(t.Context(), DeliveryJob{
		UserID:      user.ID,
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		AssetID:     sql.NullInt64{Int64: 1, Valid: true},
		Title:       "Book",
		Filename:    "Book.epub",
	})
	if err != nil {
		t.Fatalf("create converting job: %v", err)
	}
	if err := database.SetDeliveryJobStatus(t.Context(), converting.ID, DeliveryStatusConverting, ""); err != nil {
		t.Fatalf("set converting: %v", err)
	}
	queued, err := database.CreateDeliveryJob(t.Context(), DeliveryJob{
		UserID:      user.ID,
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		AssetID:     sql.NullInt64{Int64: 1, Valid: true},
		Title:       "Book",
		Filename:    "Book.epub",
	})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	if err := database.RecoverDeliveryJobs(t.Context()); err != nil {
		t.Fatalf("recover deliveries: %v", err)
	}
	job, err = GetDeliveryJob(database.Read(t.Context()), user.ID, job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != DeliveryStatusFailed || !strings.Contains(job.Error, "may have been sent") {
		t.Fatalf("interrupted sending job = %+v", job)
	}
	converting, err = GetDeliveryJob(database.Read(t.Context()), user.ID, converting.ID)
	if err != nil {
		t.Fatalf("reload converting job: %v", err)
	}
	if converting.Status != DeliveryStatusQueued || converting.Error != "" {
		t.Fatalf("recovered converting job = %+v, want queued", converting)
	}
	queued, err = GetDeliveryJob(database.Read(t.Context()), user.ID, queued.ID)
	if err != nil {
		t.Fatalf("reload queued job: %v", err)
	}
	if queued.Status != DeliveryStatusQueued || queued.Error != "" {
		t.Fatalf("untouched queued job = %+v", queued)
	}
	next, err := NextQueuedDeliveryJob(database.Read(t.Context()))
	if err != nil {
		t.Fatalf("next queued delivery: %v", err)
	}
	if next == nil || next.ID != converting.ID {
		t.Fatalf("next queued delivery = %+v, want %d", next, converting.ID)
	}
}

func TestCreateDeliveryJobPrunesCompletedHistory(t *testing.T) {
	tests := []struct {
		name      string
		completed int
	}{
		{"empty", 0},
		{"sparse history", 2},
		{"at limit", DeliveryHistoryLimit},
		{"over limit", DeliveryHistoryLimit + 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			user := mustUser(t, database, "sender", RoleReader)
			other := mustUser(t, database, "other", RoleReader)
			seed := func(id int, owner int64, status string, createdAt int) {
				t.Helper()
				mustExec(t, database, `
					INSERT INTO delivery_jobs
					    (id, user_id, device_name, device_email, preset, title, status, created_at, updated_at)
					VALUES (?, ?, 'Device', 'reader@example.test', 'generic', 'Book', ?, ?, ?)
				`, id, owner, status, createdAt, createdAt)
			}
			for i := 1; i <= tt.completed; i++ {
				status := DeliveryStatusSent
				if i%2 == 0 {
					status = DeliveryStatusFailed
				}
				// Paired creation times exercise ordering when timestamps match.
				seed(i, user.ID, status, 100+(i-1)/2)
			}
			activeStatuses := []string{DeliveryStatusQueued, DeliveryStatusConverting, DeliveryStatusSending}
			firstActiveID := tt.completed + 1
			for i, status := range activeStatuses {
				seed(firstActiveID+i, user.ID, status, 1)
			}
			otherJobID := firstActiveID + len(activeStatuses)
			seed(otherJobID, other.ID, DeliveryStatusSent, 1)

			newJob, err := database.CreateDeliveryJob(t.Context(), DeliveryJob{
				UserID:      user.ID,
				DeviceName:  "Device",
				DeviceEmail: "reader@example.test",
				Preset:      DeliveryPresetGeneric,
				Title:       "New send",
			})
			if err != nil {
				t.Fatal(err)
			}
			if newJob.Status != DeliveryStatusQueued {
				t.Fatalf("new job = %+v, want queued", newJob)
			}
			for i := 1; i <= tt.completed; i++ {
				job, err := GetDeliveryJobByID(database.Read(t.Context()), int64(i))
				if i <= tt.completed-DeliveryHistoryLimit {
					if !errors.Is(err, ErrDeliveryJobNotFound) {
						t.Fatalf("pruned job %d = %+v, err %v; want removed", i, job, err)
					}
				} else if err != nil {
					t.Fatalf("retained job %d: %v", i, err)
				}
			}
			for i, status := range activeStatuses {
				job, err := GetDeliveryJobByID(database.Read(t.Context()), int64(firstActiveID+i))
				if err != nil || job.Status != status {
					t.Fatalf("active job = %+v, err %v; want %s", job, err, status)
				}
			}
			if _, err := GetDeliveryJob(database.Read(t.Context()), other.ID, int64(otherJobID)); err != nil {
				t.Fatalf("other user's history: %v", err)
			}
			jobs, err := ListDeliveryJobs(database.Read(t.Context()), user.ID, DeliveryHistoryLimit+1)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := min(tt.completed+len(activeStatuses)+1, DeliveryHistoryLimit)
			if len(jobs) != wantCount {
				t.Fatalf("history length = %d, want %d", len(jobs), wantCount)
			}
			if jobs[0].ID != newJob.ID {
				t.Fatalf("first history job = %d, want new job %d", jobs[0].ID, newJob.ID)
			}
			for i := 2; i <= min(tt.completed, DeliveryHistoryLimit-1); i++ {
				if jobs[i].ID >= jobs[i-1].ID {
					t.Fatalf("history out of order: %d before %d", jobs[i-1].ID, jobs[i].ID)
				}
			}
		})
	}
}
