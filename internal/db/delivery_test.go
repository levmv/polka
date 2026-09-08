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
		BookID:      1,
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
		BookID:      1,
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
		BookID:      1,
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
