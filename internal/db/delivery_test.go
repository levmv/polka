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
	user, err := database.CreateUser("alice", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

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
	first, err = database.GetDeliveryDevice(user.ID, first.ID)
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
	first, err = database.GetDeliveryDevice(user.ID, first.ID)
	if err != nil {
		t.Fatalf("reload promoted first: %v", err)
	}
	if !first.IsDefault {
		t.Fatalf("remaining device should be promoted: %+v", first)
	}
}

func TestDeliveryWorkForPlanAppliesScope(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(user.ID, ShelfShared, "Allowed", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('allowed', 'Allowed', 'Allowed');
		INSERT INTO works (id, title, sort_title) VALUES ('blocked', 'Blocked', 'Blocked');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, current_size, is_primary)
			VALUES ('asset_allowed', 'allowed', 'a.epub', 'a.epub', '.epub', 'epub', 100, 1);
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, current_size, is_primary)
			VALUES ('asset_blocked', 'blocked', 'b.epub', 'b.epub', '.epub', 'epub', 100, 1);
	`); err != nil {
		t.Fatalf("seed works/assets: %v", err)
	}
	if err := database.AddBookToShelf(shelf.ID, "", "allowed"); err != nil {
		t.Fatalf("add allowed to shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}

	work, assets, err := database.DeliveryWorkForPlan(scope, "allowed")
	if err != nil {
		t.Fatalf("allowed work: %v", err)
	}
	if work.ID != "allowed" || len(assets) != 1 || assets[0].ID != "asset_allowed" {
		t.Fatalf("allowed work/assets = %+v %+v", work, assets)
	}
	if _, _, err := database.DeliveryWorkForPlan(scope, "blocked"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("blocked err = %v, want sql.ErrNoRows", err)
	}
}

func TestDeliveryJobLifecycle(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("alice", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Book', 'Book');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format)
			VALUES ('a1', 'w1', 'a.epub', 'a.epub', '.epub', 'epub');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	device, err := database.CreateDeliveryDevice(context.Background(), user.ID, "Kindle", "alice@kindle.com", DeliveryPresetKindle, true)
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	job, err := database.CreateDeliveryJob(DeliveryJob{
		UserID:      user.ID,
		DeviceID:    sql.NullString{String: device.ID, Valid: true},
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		WorkID:      "w1",
		AssetID:     sql.NullString{String: "a1", Valid: true},
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
	if err := database.SetDeliveryJobStatus(job.ID, DeliveryStatusSending, ""); err != nil {
		t.Fatalf("set sending: %v", err)
	}
	converting, err := database.CreateDeliveryJob(DeliveryJob{
		ID:          "dj_converting",
		UserID:      user.ID,
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		WorkID:      "w1",
		AssetID:     sql.NullString{String: "a1", Valid: true},
		Title:       "Book",
		Filename:    "Book.epub",
	})
	if err != nil {
		t.Fatalf("create converting job: %v", err)
	}
	if err := database.SetDeliveryJobStatus(converting.ID, DeliveryStatusConverting, ""); err != nil {
		t.Fatalf("set converting: %v", err)
	}
	queued, err := database.CreateDeliveryJob(DeliveryJob{
		ID:          "dj_queued",
		UserID:      user.ID,
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		WorkID:      "w1",
		AssetID:     sql.NullString{String: "a1", Valid: true},
		Title:       "Book",
		Filename:    "Book.epub",
	})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	if err := database.RecoverDeliveryJobs(); err != nil {
		t.Fatalf("recover deliveries: %v", err)
	}
	job, err = database.GetDeliveryJob(user.ID, job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != DeliveryStatusFailed || !strings.Contains(job.Error, "may have been sent") {
		t.Fatalf("interrupted sending job = %+v", job)
	}
	converting, err = database.GetDeliveryJob(user.ID, converting.ID)
	if err != nil {
		t.Fatalf("reload converting job: %v", err)
	}
	if converting.Status != DeliveryStatusQueued || converting.Error != "" {
		t.Fatalf("recovered converting job = %+v, want queued", converting)
	}
	queued, err = database.GetDeliveryJob(user.ID, queued.ID)
	if err != nil {
		t.Fatalf("reload queued job: %v", err)
	}
	if queued.Status != DeliveryStatusQueued || queued.Error != "" {
		t.Fatalf("untouched queued job = %+v", queued)
	}
	next, err := database.NextQueuedDeliveryJob()
	if err != nil {
		t.Fatalf("next queued delivery: %v", err)
	}
	if next == nil || next.ID != converting.ID {
		t.Fatalf("next queued delivery = %+v, want %s", next, converting.ID)
	}
}
