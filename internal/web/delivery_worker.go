package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/delivery"
	"github.com/levmv/polka/internal/format"
)

const (
	deliveryConversionTimeout = 2 * time.Minute
	deliveryStatusTimeout     = 5 * time.Second
)

type deliveryTransport interface {
	Send(ctx context.Context, copy delivery.DeliveryCopy, profile delivery.SMTPProfile) error
}

type smtpDeliveryTransport struct{}

func (smtpDeliveryTransport) Send(ctx context.Context, copy delivery.DeliveryCopy, profile delivery.SMTPProfile) error {
	return delivery.SendSMTP(ctx, copy, profile)
}

func (s *Server) wakeDeliveryWorker() {
	select {
	case s.deliveryWake <- struct{}{}:
	default:
	}
}

func (s *Server) runDeliveryWorker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := db.NextQueuedDeliveryJob(s.db.Read(ctx))
		if err != nil {
			log.Printf("delivery worker: %v", err)
			if !s.waitForDeliveryWork(ctx, time.Second) {
				return
			}
			continue
		}
		if job == nil {
			if !s.waitForDeliveryWork(ctx, 0) {
				return
			}
			continue
		}
		if err := s.runDeliveryJob(ctx, job.ID); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("delivery job %d: %v", job.ID, err)
			// A DB/configuration error can leave the row queued. Avoid a hot
			// retry loop while still reacting immediately to newly queued work.
			if !s.waitForDeliveryWork(ctx, time.Second) {
				return
			}
		}
	}
}

func (s *Server) waitForDeliveryWork(ctx context.Context, retryAfter time.Duration) bool {
	if retryAfter <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-s.deliveryWake:
			return true
		}
	}
	timer := time.NewTimer(retryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.deliveryWake:
		return true
	case <-timer.C:
		return true
	}
}

func (s *Server) runDeliveryJob(ctx context.Context, jobID int64) error {
	transport := s.deliveryTransport
	if transport == nil {
		return fmt.Errorf("delivery transport is not configured")
	}

	job, err := db.GetDeliveryJobByID(s.db.Read(ctx), jobID)
	if err != nil {
		return err
	}
	if !job.AssetID.Valid {
		return s.failDeliveryJob(ctx, job.ID, deliveryMessageFileMissing)
	}
	cfg, _, err := s.deliveryEmailConfig(ctx)
	if err != nil {
		return s.failDeliveryJobWithCause(ctx, job.ID, deliveryMessageFailed, "load email settings", err)
	}
	if !cfg.Configured() {
		return s.failDeliveryJob(ctx, job.ID, deliveryMessageNotConfigured)
	}
	scope, err := db.VisibilityScopeForUser(s.db.Read(ctx), job.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.failDeliveryJob(ctx, job.ID, deliveryMessageNoLongerVisible)
	}
	if err != nil {
		return s.failDeliveryJobWithCause(ctx, job.ID, deliveryMessageFailed, "load visibility scope", err)
	}
	allowed, err := db.CanAccessAsset(s.db.Read(ctx), scope, job.AssetID.Int64)
	if err != nil {
		return s.failDeliveryJobWithCause(ctx, job.ID, deliveryMessageFailed, "check asset visibility", err)
	}
	if !allowed {
		return s.failDeliveryJob(ctx, job.ID, deliveryMessageNoLongerVisible)
	}

	copy, cleanup, err := s.prepareDeliveryCopy(ctx, *job)
	if err != nil {
		if ctx.Err() != nil {
			return s.requeueInterruptedDelivery(ctx, job.ID, ctx.Err())
		}
		return s.failDeliveryJobFromError(ctx, job.ID, "prepare file", err)
	}
	defer cleanup()
	if err := ctx.Err(); err != nil {
		return s.requeueInterruptedDelivery(ctx, job.ID, err)
	}
	if !delivery.FitsAttachmentLimit(copy.Size, delivery.Preset(job.Preset), cfg.AttachmentLimitMB) {
		return s.failDeliveryJob(ctx, job.ID, fmt.Sprintf("File is too large for email delivery (%s, limit %s).", delivery.FormatBytesMB(copy.Size), delivery.FormatBytesMB(delivery.EffectiveLimitBytes(delivery.Preset(job.Preset), cfg.AttachmentLimitMB))))
	}
	_ = s.db.SetDeliveryJobSize(ctx, job.ID, copy.Size)
	if err := s.db.SetDeliveryJobStatus(ctx, job.ID, db.DeliveryStatusSending, ""); err != nil {
		return err
	}
	err = transport.Send(ctx, copy, delivery.SMTPProfile{
		Config:  cfg,
		To:      job.DeviceEmail,
		Subject: job.Title,
	})
	// Sending has an external effect. Persist its outcome even if shutdown
	// canceled the worker, so an already sent message is not queued again.
	resultCtx, cancelResult := context.WithTimeout(context.WithoutCancel(ctx), deliveryStatusTimeout)
	defer cancelResult()
	if err != nil {
		if ctx.Err() != nil {
			return s.failDeliveryJob(resultCtx, job.ID, deliveryMessageSendInterrupted)
		}
		return s.failDeliveryJobFromError(resultCtx, job.ID, "send", err)
	}
	return s.db.SetDeliveryJobStatus(resultCtx, job.ID, db.DeliveryStatusSent, "")
}

func (s *Server) requeueInterruptedDelivery(ctx context.Context, jobID int64, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryStatusTimeout)
	defer cancel()
	if err := s.db.SetDeliveryJobStatus(ctx, jobID, db.DeliveryStatusQueued, ""); err != nil {
		return fmt.Errorf("%w; return delivery to queue: %v", cause, err)
	}
	return cause
}

func (s *Server) prepareDeliveryCopy(ctx context.Context, job db.DeliveryJob) (delivery.DeliveryCopy, func(), error) {
	asset, src, err := s.openDeliverySource(ctx, job.AssetID.Int64)
	if err != nil {
		return delivery.DeliveryCopy{}, func() {}, err
	}
	sourceOwned := true
	defer func() {
		if sourceOwned {
			_ = src.Close()
		}
	}()

	if !job.Target.Valid || job.Target.String == "" {
		sourceOwned = false
		tmpPath, size, cleanup, err := s.prepareDeliveryTemp(src, asset.Extension, func(dst, src *os.File) error {
			_, err := io.Copy(dst, src)
			return err
		})
		if err != nil {
			return delivery.DeliveryCopy{}, func() {}, err
		}
		mediaType := format.MediaTypeForExtension(asset.Extension)
		return delivery.DeliveryCopy{Path: tmpPath, Filename: job.Filename, MediaType: mediaType, Size: size}, cleanup, nil
	}

	target := converter.Target(job.Target.String)
	if !converter.CanConvert(asset.Format, target) {
		return delivery.DeliveryCopy{}, func() {}, newDeliveryPrepError(deliveryMessageConversionMissing, nil)
	}
	if err := s.db.SetDeliveryJobStatus(ctx, job.ID, db.DeliveryStatusConverting, ""); err != nil {
		return delivery.DeliveryCopy{}, func() {}, err
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryConversionTimeout)
	defer cancel()
	convertOpts, err := s.assetConversionOptions(ctx, asset)
	if err != nil {
		return delivery.DeliveryCopy{}, func() {}, newDeliveryPrepError(deliveryMessagePrepareFailed, err)
	}
	sourceOwned = false
	tmpPath, size, cleanup, err := s.prepareDeliveryTemp(src, converter.TargetExtension(target), func(dst, src *os.File) error {
		info, err := src.Stat()
		if err != nil {
			return newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("stat source file for conversion: %w", err))
		}
		if err := s.withConversionSlot(ctx, func() error {
			return converter.ConvertContextWithOptions(ctx, dst, src, asset.Format, info.Size(), target, convertOpts)
		}); err != nil {
			return newDeliveryPrepError(deliveryMessageConversionFailed, err)
		}
		return nil
	})
	if err != nil {
		return delivery.DeliveryCopy{}, func() {}, err
	}
	return delivery.DeliveryCopy{
		Path:      tmpPath,
		Filename:  job.Filename,
		MediaType: converter.TargetMediaType(target),
		Size:      size,
	}, cleanup, nil
}

// openDeliverySource normally reads without taking the storage mutation slot.
// If the open loses the narrow race with a managed-file move, wait for that
// mutation, resolve the asset from SQLite again, and retry once while the path
// is stable. The slot is released as soon as the descriptor is open; copying or
// conversion can then proceed without blocking unrelated storage work.
func (s *Server) openDeliverySource(ctx context.Context, assetID int64) (assetFileRow, *os.File, error) {
	asset, src, err := s.openDeliverySourceOnce(ctx, assetID)
	if !errors.Is(err, os.ErrNotExist) {
		return asset, src, err
	}

	releaseStorageSlot, slotErr := s.acquireStorageWorkSlot(ctx)
	if slotErr != nil {
		return assetFileRow{}, nil, slotErr
	}
	defer releaseStorageSlot()
	return s.openDeliverySourceOnce(ctx, assetID)
}

func (s *Server) openDeliverySourceOnce(ctx context.Context, assetID int64) (assetFileRow, *os.File, error) {
	asset, err := s.assetFile(ctx, assetID)
	if errors.Is(err, sql.ErrNoRows) {
		return assetFileRow{}, nil, newDeliveryPrepError(deliveryMessageFileMissing, nil)
	}
	if err != nil {
		return assetFileRow{}, nil, newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("resolve asset %d: %w", assetID, err))
	}
	fullPath, err := s.managedRoot().Resolve(asset.StoragePath)
	if err != nil {
		return assetFileRow{}, nil, newDeliveryPrepError(deliveryMessageFileMissing, fmt.Errorf("resolve storage path for asset %d: %w", assetID, err))
	}
	src, err := os.Open(fullPath)
	if os.IsNotExist(err) {
		return assetFileRow{}, nil, newDeliveryPrepError(deliveryMessageFileMissing, err)
	}
	if err != nil {
		return assetFileRow{}, nil, newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("open source file: %w", err))
	}
	return asset, src, nil
}

// prepareDeliveryTemp takes ownership of src and closes it before returning.
func (s *Server) prepareDeliveryTemp(src *os.File, ext string, write func(dst, src *os.File) error) (string, int64, func(), error) {
	tmp, tmpPath, cleanup, err := s.createDeliveryTempFile(ext)
	if err != nil {
		_ = src.Close()
		return "", 0, func() {}, newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("create delivery temp file: %w", err))
	}

	writeErr := write(tmp, src)
	srcErr := src.Close()
	closeErr := tmp.Close()
	if writeErr != nil {
		cleanup()
		return "", 0, func() {}, wrapDeliveryPrepError(deliveryMessagePrepareFailed, writeErr)
	}
	if srcErr != nil {
		cleanup()
		return "", 0, func() {}, newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("close source file: %w", srcErr))
	}
	if closeErr != nil {
		cleanup()
		return "", 0, func() {}, newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("close delivery temp file: %w", closeErr))
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		cleanup()
		return "", 0, func() {}, newDeliveryPrepError(deliveryMessagePrepareFailed, fmt.Errorf("stat delivery temp file: %w", err))
	}
	return tmpPath, info.Size(), cleanup, nil
}

func (s *Server) createDeliveryTempFile(ext string) (*os.File, string, func(), error) {
	tmpDir := filepath.Join(s.dataDir, "tmp", "delivery")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, "", func() {}, err
	}
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	tmp, err := os.CreateTemp(tmpDir, "delivery-*"+ext)
	if err != nil {
		return nil, "", func() {}, err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	return tmp, tmpPath, cleanup, nil
}

func (s *Server) failDeliveryJob(ctx context.Context, jobID int64, message string) error {
	if message == "" {
		message = deliveryMessageFailed
	}
	return s.db.SetDeliveryJobStatus(ctx, jobID, db.DeliveryStatusFailed, message)
}

func (s *Server) failDeliveryJobWithCause(ctx context.Context, jobID int64, message, action string, err error) error {
	log.Printf("delivery job %d: %s: %v", jobID, action, err)
	if setErr := s.failDeliveryJob(ctx, jobID, message); setErr != nil {
		return fmt.Errorf("%s: %w; mark delivery failed: %v", action, err, setErr)
	}
	return nil
}

func (s *Server) failDeliveryJobFromError(ctx context.Context, jobID int64, action string, err error) error {
	if userErr, ok := errors.AsType[deliveryUserError](err); ok {
		return s.failDeliveryJobWithCause(ctx, jobID, userErr.UserMessage(), action, err)
	}
	return s.failDeliveryJobWithCause(ctx, jobID, deliveryMessageFailed, action, err)
}

type deliveryUserError interface {
	error
	UserMessage() string
}

type deliveryPrepError struct {
	message string
	cause   error
}

func newDeliveryPrepError(message string, cause error) error {
	if message == "" {
		message = deliveryMessageFailed
	}
	return &deliveryPrepError{message: message, cause: cause}
}

func wrapDeliveryPrepError(message string, err error) error {
	if _, ok := errors.AsType[deliveryUserError](err); ok {
		return err
	}
	return newDeliveryPrepError(message, err)
}

func (e *deliveryPrepError) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}

func (e *deliveryPrepError) Unwrap() error {
	return e.cause
}

func (e *deliveryPrepError) UserMessage() string {
	return e.message
}
