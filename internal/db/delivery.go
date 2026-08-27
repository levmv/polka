package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/id"
)

const (
	DeliveryPresetKindle     = "kindle"
	DeliveryPresetPocketBook = "pocketbook"
	DeliveryPresetGeneric    = "generic"

	DeliveryStatusQueued     = "queued"
	DeliveryStatusConverting = "converting"
	DeliveryStatusSending    = "sending"
	DeliveryStatusSent       = "sent"
	DeliveryStatusFailed     = "failed"
)

const (
	deliveryDeviceColumns = `id, user_id, name, email, preset, is_default, created_at, updated_at`
	deliveryJobColumns    = `id, user_id, device_id, device_name, device_email, preset, work_id,
		asset_id, title, target, filename, size_bytes, status, error,
		created_at, updated_at, sent_at`
)

var (
	ErrDeliveryDeviceNotFound     = errors.New("delivery device not found")
	ErrDeliveryDeviceNameExists   = errors.New("delivery device name already exists")
	ErrDeliveryDeviceNameMissing  = errors.New("delivery device name is required")
	ErrDeliveryDeviceEmailMissing = errors.New("delivery device email is required")
	ErrInvalidDeliveryPreset      = fmt.Errorf("delivery preset must be %s, %s, or %s", DeliveryPresetKindle, DeliveryPresetPocketBook, DeliveryPresetGeneric)
	ErrDeliveryJobNotFound        = errors.New("delivery job not found")
)

type DeliveryDevice struct {
	ID        string
	UserID    string
	Name      string
	Email     string
	Preset    string
	IsDefault bool
	CreatedAt int64
	UpdatedAt int64
}

type DeliveryJob struct {
	ID          string
	UserID      string
	DeviceID    sql.NullString
	DeviceName  string
	DeviceEmail string
	Preset      string
	WorkID      string
	AssetID     sql.NullString
	Title       string
	Target      sql.NullString
	Filename    string
	SizeBytes   sql.NullInt64
	Status      string
	Error       string
	CreatedAt   int64
	UpdatedAt   int64
	SentAt      sql.NullInt64
}

type DeliveryWorkRow struct {
	ID      string
	Title   string
	Authors string
}

type DeliveryAssetRow struct {
	ID        string
	Filename  string
	Extension string
	Format    format.Format
	Size      int64
	IsPrimary bool
}

func ValidDeliveryPreset(preset string) bool {
	switch preset {
	case DeliveryPresetKindle, DeliveryPresetPocketBook, DeliveryPresetGeneric:
		return true
	default:
		return false
	}
}

func (db *DB) ListDeliveryDevices(userID string) ([]DeliveryDevice, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	rows, err := db.Query(`
		SELECT `+deliveryDeviceColumns+`
		FROM delivery_devices
		WHERE user_id = ?
		ORDER BY is_default DESC, name COLLATE NOCASE ASC, created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list delivery devices: %w", err)
	}
	defer rows.Close()

	var devices []DeliveryDevice
	for rows.Next() {
		device, err := scanDeliveryDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (db *DB) GetDeliveryDevice(userID, deviceID string) (*DeliveryDevice, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	device, err := scanDeliveryDevice(db.QueryRow(`
		SELECT `+deliveryDeviceColumns+`
		FROM delivery_devices
		WHERE user_id = ? AND id = ?
	`, userID, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery device: %w", err)
	}
	return &device, nil
}

func (db *DB) DefaultDeliveryDevice(userID string) (*DeliveryDevice, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	device, err := scanDeliveryDevice(db.QueryRow(`
		SELECT `+deliveryDeviceColumns+`
		FROM delivery_devices
		WHERE user_id = ?
		ORDER BY is_default DESC, created_at DESC
		LIMIT 1
	`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get default delivery device: %w", err)
	}
	return &device, nil
}

func (db *DB) CreateDeliveryDevice(ctx context.Context, userID, name, email, preset string, isDefault bool) (*DeliveryDevice, error) {
	if err := validateDeliveryDeviceInput(userID, name, email, preset); err != nil {
		return nil, err
	}
	deviceID := id.New(id.DeliveryDevice)
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM delivery_devices WHERE user_id = ?", userID).Scan(&count); err != nil {
			return fmt.Errorf("count delivery devices: %w", err)
		}
		if count == 0 {
			isDefault = true
		}
		if isDefault {
			if _, err := tx.Exec("UPDATE delivery_devices SET is_default = 0, updated_at = unixepoch() WHERE user_id = ?", userID); err != nil {
				return fmt.Errorf("clear delivery default: %w", err)
			}
		}
		_, err := tx.Exec(`
			INSERT INTO delivery_devices (id, user_id, name, email, preset, is_default)
			VALUES (?, ?, ?, ?, ?, ?)
		`, deviceID, userID, strings.TrimSpace(name), strings.TrimSpace(email), preset, boolInt(isDefault))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDeliveryDeviceNameExists
			}
			return fmt.Errorf("insert delivery device: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db.GetDeliveryDevice(userID, deviceID)
}

func (db *DB) UpdateDeliveryDevice(ctx context.Context, userID string, device DeliveryDevice) (*DeliveryDevice, error) {
	if device.ID == "" {
		return nil, ErrDeliveryDeviceNotFound
	}
	if err := validateDeliveryDeviceInput(userID, device.Name, device.Email, device.Preset); err != nil {
		return nil, err
	}
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		if device.IsDefault {
			if _, err := tx.Exec("UPDATE delivery_devices SET is_default = 0, updated_at = unixepoch() WHERE user_id = ?", userID); err != nil {
				return fmt.Errorf("clear delivery default: %w", err)
			}
		}
		res, err := tx.Exec(`
			UPDATE delivery_devices
			SET name = ?, email = ?, preset = ?, is_default = ?, updated_at = unixepoch()
			WHERE user_id = ? AND id = ?
		`, strings.TrimSpace(device.Name), strings.TrimSpace(device.Email), device.Preset, boolInt(device.IsDefault), userID, device.ID)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDeliveryDeviceNameExists
			}
			return fmt.Errorf("update delivery device: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDeliveryDeviceNotFound
		}
		if !device.IsDefault {
			if err := ensureDeliveryDefault(tx, userID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db.GetDeliveryDevice(userID, device.ID)
}

func (db *DB) DeleteDeliveryDevice(ctx context.Context, userID, deviceID string) error {
	if userID == "" {
		return ErrUserIDRequired
	}
	return db.Transact(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM delivery_devices WHERE user_id = ? AND id = ?", userID, deviceID)
		if err != nil {
			return fmt.Errorf("delete delivery device: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDeliveryDeviceNotFound
		}
		return ensureDeliveryDefault(tx, userID)
	})
}

func ensureDeliveryDefault(tx *sql.Tx, userID string) error {
	var defaults int
	if err := tx.QueryRow("SELECT COUNT(*) FROM delivery_devices WHERE user_id = ? AND is_default = 1", userID).Scan(&defaults); err != nil {
		return fmt.Errorf("count delivery defaults: %w", err)
	}
	if defaults > 0 {
		return nil
	}
	_, err := tx.Exec(`
		UPDATE delivery_devices
		SET is_default = 1, updated_at = unixepoch()
		WHERE id = (
			SELECT id FROM delivery_devices
			WHERE user_id = ?
			ORDER BY created_at DESC
			LIMIT 1
		)
	`, userID)
	if err != nil {
		return fmt.Errorf("promote delivery default: %w", err)
	}
	return nil
}

func validateDeliveryDeviceInput(userID, name, email, preset string) error {
	if userID == "" {
		return ErrUserIDRequired
	}
	if strings.TrimSpace(name) == "" {
		return ErrDeliveryDeviceNameMissing
	}
	if strings.TrimSpace(email) == "" {
		return ErrDeliveryDeviceEmailMissing
	}
	if !ValidDeliveryPreset(preset) {
		return ErrInvalidDeliveryPreset
	}
	return nil
}

func (db *DB) DeliveryWorkForPlan(scope VisibilityScope, workID string) (DeliveryWorkRow, []DeliveryAssetRow, error) {
	where, args := scope.AppendWorkWhere("w.id = ? AND w.deleted_at IS NULL", "w.id", workID)
	var work DeliveryWorkRow
	err := db.QueryRow(`
		SELECT w.id, w.title, `+colAuthors+`
		FROM works w
		WHERE `+where+`
		LIMIT 1
	`, args...).Scan(&work.ID, &work.Title, &work.Authors)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryWorkRow{}, nil, sql.ErrNoRows
	}
	if err != nil {
		return DeliveryWorkRow{}, nil, fmt.Errorf("get delivery work: %w", err)
	}

	rows, err := db.Query(`
		SELECT id, filename, extension, format, COALESCE(current_size, original_size, 0), is_primary
		FROM assets
		WHERE work_id = ?
		ORDER BY is_primary DESC, id ASC
	`, work.ID)
	if err != nil {
		return DeliveryWorkRow{}, nil, fmt.Errorf("list delivery assets: %w", err)
	}
	defer rows.Close()

	var assets []DeliveryAssetRow
	for rows.Next() {
		var row DeliveryAssetRow
		var formatKey string
		var isPrimary int
		if err := rows.Scan(&row.ID, &row.Filename, &row.Extension, &formatKey, &row.Size, &isPrimary); err != nil {
			return DeliveryWorkRow{}, nil, fmt.Errorf("scan delivery asset: %w", err)
		}
		row.Format = format.FormatFromKey(formatKey)
		row.IsPrimary = isPrimary != 0
		assets = append(assets, row)
	}
	if err := rows.Err(); err != nil {
		return DeliveryWorkRow{}, nil, err
	}
	return work, assets, nil
}

func (db *DB) CreateDeliveryJob(job DeliveryJob) (*DeliveryJob, error) {
	if job.ID == "" {
		job.ID = id.New(id.DeliveryJob)
	}
	if job.Status == "" {
		job.Status = DeliveryStatusQueued
	}
	_, err := db.Exec(`
		INSERT INTO delivery_jobs (
			id, user_id, device_id, device_name, device_email, preset, work_id,
			asset_id, title, target, filename, size_bytes, status, error
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.UserID, job.DeviceID, job.DeviceName, job.DeviceEmail, job.Preset,
		job.WorkID, job.AssetID, job.Title, job.Target, job.Filename, job.SizeBytes,
		job.Status, job.Error)
	if err != nil {
		return nil, fmt.Errorf("create delivery job: %w", err)
	}
	return db.GetDeliveryJob(job.UserID, job.ID)
}

func (db *DB) GetDeliveryJob(userID, jobID string) (*DeliveryJob, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	job, err := scanDeliveryJobRow(db.QueryRow(`
		SELECT `+deliveryJobColumns+`
		FROM delivery_jobs
		WHERE user_id = ? AND id = ?
	`, userID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery job: %w", err)
	}
	return job, nil
}

func (db *DB) GetDeliveryJobByID(jobID string) (*DeliveryJob, error) {
	job, err := scanDeliveryJobRow(db.QueryRow(`
		SELECT `+deliveryJobColumns+`
		FROM delivery_jobs
		WHERE id = ?
	`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery job by id: %w", err)
	}
	return job, nil
}

// NextQueuedDeliveryJob returns the oldest durable delivery waiting for the
// single server worker. The writer lease guarantees there is only one worker
// process, so a separate claim/lock protocol would add no useful safety here.
func (db *DB) NextQueuedDeliveryJob() (*DeliveryJob, error) {
	job, err := scanDeliveryJobRow(db.QueryRow(`
		SELECT ` + deliveryJobColumns + `
		FROM delivery_jobs
		WHERE status = 'queued'
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get next queued delivery job: %w", err)
	}
	return job, nil
}

func (db *DB) ListDeliveryJobs(userID string, limit int) ([]DeliveryJob, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	rows, err := db.Query(`
		SELECT `+deliveryJobColumns+`
		FROM delivery_jobs
		WHERE user_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list delivery jobs: %w", err)
	}
	defer rows.Close()

	var jobs []DeliveryJob
	for rows.Next() {
		job, err := scanDeliveryJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (db *DB) SetDeliveryJobStatus(jobID, status, errorMessage string) error {
	if !validDeliveryStatus(status) {
		return fmt.Errorf("invalid delivery status %q", status)
	}
	sentExpr := "sent_at"
	if status == DeliveryStatusSent {
		sentExpr = "unixepoch()"
	}
	res, err := db.Exec(`
		UPDATE delivery_jobs
		SET status = ?, error = ?, updated_at = unixepoch(), sent_at = `+sentExpr+`
		WHERE id = ?
	`, status, errorMessage, jobID)
	if err != nil {
		return fmt.Errorf("set delivery job status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDeliveryJobNotFound
	}
	return nil
}

func (db *DB) SetDeliveryJobSize(jobID string, size int64) error {
	_, err := db.Exec("UPDATE delivery_jobs SET size_bytes = ?, updated_at = unixepoch() WHERE id = ?", size, jobID)
	if err != nil {
		return fmt.Errorf("set delivery job size: %w", err)
	}
	return nil
}

// RecoverDeliveryJobs preserves work which is safe to repeat while refusing to
// automatically duplicate a possibly completed SMTP send.
func (db *DB) RecoverDeliveryJobs() error {
	_, err := db.Exec(`
		UPDATE delivery_jobs
		SET status = CASE status
		        WHEN 'converting' THEN 'queued'
		        ELSE 'failed'
		    END,
		    error = CASE status
		        WHEN 'converting' THEN ''
		        ELSE 'Delivery was interrupted while sending; the message may have been sent.'
		    END,
		    updated_at = unixepoch()
		WHERE status IN ('converting', 'sending')
	`)
	if err != nil {
		return fmt.Errorf("recover delivery jobs: %w", err)
	}
	return nil
}

func validDeliveryStatus(status string) bool {
	switch status {
	case DeliveryStatusQueued, DeliveryStatusConverting, DeliveryStatusSending, DeliveryStatusSent, DeliveryStatusFailed:
		return true
	default:
		return false
	}
}

func scanDeliveryDevice(row rowScanner) (DeliveryDevice, error) {
	var device DeliveryDevice
	var isDefault int
	if err := row.Scan(&device.ID, &device.UserID, &device.Name, &device.Email, &device.Preset, &isDefault, &device.CreatedAt, &device.UpdatedAt); err != nil {
		return DeliveryDevice{}, fmt.Errorf("scan delivery device: %w", err)
	}
	device.IsDefault = isDefault != 0
	return device, nil
}

func scanDeliveryJobRow(row rowScanner) (*DeliveryJob, error) {
	var job DeliveryJob
	if err := row.Scan(
		&job.ID, &job.UserID, &job.DeviceID, &job.DeviceName, &job.DeviceEmail,
		&job.Preset, &job.WorkID, &job.AssetID, &job.Title, &job.Target,
		&job.Filename, &job.SizeBytes, &job.Status, &job.Error,
		&job.CreatedAt, &job.UpdatedAt, &job.SentAt,
	); err != nil {
		return nil, err
	}
	return &job, nil
}

func scanDeliveryJob(rows *sql.Rows) (DeliveryJob, error) {
	job, err := scanDeliveryJobRow(rows)
	if err != nil {
		return DeliveryJob{}, fmt.Errorf("scan delivery job: %w", err)
	}
	return *job, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
