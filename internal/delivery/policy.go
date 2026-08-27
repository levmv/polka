package delivery

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/format"
)

type Preset string

const (
	PresetKindle     Preset = "kindle"
	PresetPocketBook Preset = "pocketbook"
	PresetGeneric    Preset = "generic"
)

const (
	DefaultAttachmentLimitMB = 25
	KindleAttachmentLimitMB  = 50
	EmailMessageOverhead     = 4096
)

type Work struct {
	ID      string
	Title   string
	Authors string
	Assets  []Asset
}

type Asset struct {
	ID        string
	Filename  string
	Extension string
	Format    format.Format
	Size      int64
	IsPrimary bool
}

type PlanOptions struct {
	Preset            Preset
	AttachmentLimitMB int
	RequestedAssetID  string
	RequestedTarget   converter.Target
}

type Plan struct {
	AssetID      string
	SourceFormat format.Format
	Target       converter.Target
	Filename     string
	MediaType    string
	SizeBytes    int64
	Converted    bool
	Reason       Reason
}

type PlanChoice struct {
	Plan    Plan
	Default bool
}

func (p Plan) Sendable() bool {
	return p.AssetID != "" && p.Reason.Code == ""
}

type Reason struct {
	Code    string
	Message string
}

const (
	ReasonNoCompatibleFormat = "no_compatible_format"
	ReasonTooLarge           = "too_large"
	ReasonAssetMissing       = "asset_missing"
	ReasonConversionMissing  = "conversion_missing"
)

func ValidPreset(preset Preset) bool {
	switch preset {
	case PresetKindle, PresetPocketBook, PresetGeneric:
		return true
	default:
		return false
	}
}

func PresetFromEmail(email string) Preset {
	value := strings.ToLower(strings.TrimSpace(email))
	switch {
	case strings.HasSuffix(value, "@kindle.com"),
		strings.HasSuffix(value, "@free.kindle.com"),
		strings.HasSuffix(value, "@kindle.cn"),
		strings.HasSuffix(value, "@free.kindle.cn"):
		return PresetKindle
	case strings.HasSuffix(value, "@pbsync.com"):
		return PresetPocketBook
	default:
		return PresetGeneric
	}
}

func PlanDelivery(work Work, opts PlanOptions) Plan {
	opts = normalizePlanOptions(opts)
	if len(work.Assets) == 0 {
		return noPlan(ReasonNoCompatibleFormat, "This book has no file to send.")
	}
	if opts.RequestedAssetID != "" {
		return planRequested(work, opts)
	}
	return planBest(work, opts)
}

func PlanChoices(work Work, opts PlanOptions) []PlanChoice {
	opts = normalizePlanOptions(opts)
	if len(work.Assets) == 0 {
		return nil
	}

	var choices []PlanChoice
	type choiceKey struct {
		assetID string
		target  converter.Target
	}
	seen := make(map[choiceKey]struct{})
	add := func(plan Plan, isDefault bool) {
		if !plan.Sendable() {
			return
		}
		key := choiceKey{assetID: plan.AssetID, target: plan.Target}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		choices = append(choices, PlanChoice{Plan: plan, Default: isDefault})
	}

	add(planBest(work, opts), true)
	if opts.Preset == PresetGeneric {
		for _, asset := range sortedAssets(work.Assets) {
			add(planRequested(work, PlanOptions{
				Preset:            opts.Preset,
				AttachmentLimitMB: opts.AttachmentLimitMB,
				RequestedAssetID:  asset.ID,
			}), false)
		}
	} else {
		for _, f := range directPreference(opts.Preset) {
			for _, asset := range assetsByFormat(work.Assets, f) {
				add(planRequested(work, PlanOptions{
					Preset:            opts.Preset,
					AttachmentLimitMB: opts.AttachmentLimitMB,
					RequestedAssetID:  asset.ID,
				}), false)
			}
		}
	}
	for _, target := range conversionPreference(opts.Preset) {
		for _, asset := range sortedAssets(work.Assets) {
			if !conversionAllowed(opts.Preset, asset.Format, target) {
				continue
			}
			add(planRequested(work, PlanOptions{
				Preset:            opts.Preset,
				AttachmentLimitMB: opts.AttachmentLimitMB,
				RequestedAssetID:  asset.ID,
				RequestedTarget:   target,
			}), false)
		}
	}
	return choices
}

func planRequested(work Work, opts PlanOptions) Plan {
	var selected Asset
	for _, asset := range work.Assets {
		if asset.ID == opts.RequestedAssetID {
			selected = asset
			break
		}
	}
	if selected.ID == "" {
		return noPlan(ReasonAssetMissing, "The selected file is no longer available.")
	}
	if opts.RequestedTarget != "" {
		target := opts.RequestedTarget
		if !conversionAllowed(opts.Preset, selected.Format, target) {
			return noPlan(ReasonConversionMissing, fmt.Sprintf("Cannot convert %s for this device.", format.FormatLabel(selected.Format)))
		}
		return conversionPlan(work, selected, target)
	}
	if !directAllowed(opts.Preset, selected.Format) {
		return incompatibleReason(work, opts.Preset)
	}
	if tooLarge(selected.Size, opts) {
		return tooLargeReason(selected.Size, opts)
	}
	return nativePlan(work, selected)
}

func planBest(work Work, opts PlanOptions) Plan {
	if opts.Preset == PresetGeneric {
		return noPlan(ReasonNoCompatibleFormat, "Choose a file to email for this generic device.")
	}

	var tooLargeCandidate *Asset
	for _, f := range directPreference(opts.Preset) {
		candidates := assetsByFormat(work.Assets, f)
		for i := range candidates {
			candidate := candidates[i]
			if tooLarge(candidate.Size, opts) {
				if tooLargeCandidate == nil || candidate.Size < tooLargeCandidate.Size {
					tooLargeCandidate = &candidate
				}
				continue
			}
			return nativePlan(work, candidate)
		}
	}

	for _, target := range conversionPreference(opts.Preset) {
		for _, asset := range sortedAssets(work.Assets) {
			if conversionAllowed(opts.Preset, asset.Format, target) {
				return conversionPlan(work, asset, target)
			}
		}
	}

	if tooLargeCandidate != nil {
		return tooLargeReason(tooLargeCandidate.Size, opts)
	}
	return incompatibleReason(work, opts.Preset)
}

func nativePlan(work Work, asset Asset) Plan {
	mediaType := format.MediaTypeForExtension(asset.Extension)
	return Plan{
		AssetID:      asset.ID,
		SourceFormat: asset.Format,
		Filename:     deliveryFilename(work, asset, ""),
		MediaType:    mediaType,
		SizeBytes:    asset.Size,
	}
}

func conversionPlan(work Work, asset Asset, target converter.Target) Plan {
	return Plan{
		AssetID:      asset.ID,
		SourceFormat: asset.Format,
		Target:       target,
		Filename:     deliveryFilename(work, asset, converter.TargetExtension(target)),
		MediaType:    converter.TargetMediaType(target),
		Converted:    true,
	}
}

func noPlan(code, message string) Plan {
	return Plan{Reason: Reason{Code: code, Message: message}}
}

func incompatibleReason(_ Work, _ Preset) Plan {
	return noPlan(ReasonNoCompatibleFormat, "No format this device accepts by email.")
}

func tooLargeReason(size int64, opts PlanOptions) Plan {
	limit := EffectiveLimitBytes(opts.Preset, opts.AttachmentLimitMB)
	return noPlan(ReasonTooLarge, fmt.Sprintf("File is too large for email delivery (%s, limit %s).", FormatBytesMB(size), FormatBytesMB(limit)))
}

func directAllowed(preset Preset, f format.Format) bool {
	if normalizePreset(preset) == PresetGeneric {
		return f != format.FormatUnknown
	}
	return slices.Contains(directPreference(preset), f)
}

func conversionAllowed(preset Preset, from format.Format, target converter.Target) bool {
	if target == "" || !converter.CanConvert(from, target) {
		return false
	}
	for _, allowed := range conversionPreference(preset) {
		if target == allowed {
			if preset == PresetKindle && target == converter.TargetEPUB {
				return kindleEPUBSource(from)
			}
			if preset == PresetPocketBook && target == converter.TargetEPUB {
				return pocketBookEPUBSource(from)
			}
			return true
		}
	}
	return false
}

func directPreference(preset Preset) []format.Format {
	switch normalizePreset(preset) {
	case PresetKindle:
		return []format.Format{
			format.FormatEPUB,
			format.FormatPDF,
			format.FormatDOCX,
			format.FormatRTF,
			format.FormatTXT,
			format.FormatHTML,
		}
	case PresetPocketBook:
		return []format.Format{
			format.FormatEPUB,
			format.FormatFB2,
			format.FormatPDF,
			format.FormatMOBI,
			format.FormatAZW,
			format.FormatPRC,
			format.FormatDJVU,
			format.FormatDOCX,
			format.FormatRTF,
			format.FormatTXT,
			format.FormatHTML,
			format.FormatCBZ,
		}
	default:
		return nil
	}
}

func conversionPreference(preset Preset) []converter.Target {
	switch normalizePreset(preset) {
	case PresetKindle, PresetPocketBook:
		return []converter.Target{converter.TargetEPUB, converter.TargetPDF}
	case PresetGeneric:
		return []converter.Target{converter.TargetEPUB, converter.TargetPDF, converter.TargetKEPUB}
	default:
		return nil
	}
}

func kindleEPUBSource(f format.Format) bool {
	switch f {
	case format.FormatFB2, format.FormatTXT, format.FormatTXTZ, format.FormatMarkdown,
		format.FormatHTML, format.FormatXHTML, format.FormatHTMLZ,
		format.FormatMOBI, format.FormatAZW, format.FormatAZW3, format.FormatPRC, format.FormatPDB:
		return true
	default:
		return false
	}
}

func pocketBookEPUBSource(f format.Format) bool {
	switch f {
	case format.FormatTXT, format.FormatTXTZ, format.FormatMarkdown,
		format.FormatHTML, format.FormatXHTML, format.FormatHTMLZ,
		format.FormatMOBI, format.FormatAZW, format.FormatAZW3, format.FormatPRC, format.FormatPDB:
		return true
	default:
		return false
	}
}

func assetsByFormat(assets []Asset, f format.Format) []Asset {
	var out []Asset
	for _, asset := range sortedAssets(assets) {
		if asset.Format == f {
			out = append(out, asset)
		}
	}
	return out
}

func sortedAssets(assets []Asset) []Asset {
	out := append([]Asset(nil), assets...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsPrimary != out[j].IsPrimary {
			return out[i].IsPrimary
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func tooLarge(size int64, opts PlanOptions) bool {
	if size <= 0 {
		return false
	}
	limit := EffectiveLimitBytes(opts.Preset, opts.AttachmentLimitMB)
	if limit <= 0 {
		return false
	}
	return EncodedSize(size) > limit
}

func EffectiveLimitBytes(preset Preset, attachmentLimitMB int) int64 {
	adminLimit := int64(attachmentLimitMB) * 1024 * 1024
	if adminLimit <= 0 {
		adminLimit = int64(DefaultAttachmentLimitMB) * 1024 * 1024
	}
	presetLimit := int64(0)
	switch normalizePreset(preset) {
	case PresetKindle, PresetPocketBook:
		presetLimit = int64(KindleAttachmentLimitMB) * 1024 * 1024
	}
	if presetLimit > 0 && presetLimit < adminLimit {
		return presetLimit
	}
	return adminLimit
}

func FitsAttachmentLimit(rawBytes int64, preset Preset, attachmentLimitMB int) bool {
	if rawBytes <= 0 {
		return true
	}
	limit := EffectiveLimitBytes(preset, attachmentLimitMB)
	return limit <= 0 || EncodedSize(rawBytes) <= limit
}

func EncodedSize(raw int64) int64 {
	if raw <= 0 {
		return 0
	}
	// Keep this aligned with base64LineWriter: MIME base64 is wrapped at 76
	// bytes with CRLF line endings, plus a conservative header/boundary budget.
	encoded := ((raw + 2) / 3) * 4
	lines := (encoded + 75) / 76
	return encoded + lines*2 + EmailMessageOverhead
}

func deliveryFilename(work Work, asset Asset, forcedExt string) string {
	base := strings.TrimSpace(work.Title)
	if strings.TrimSpace(work.Authors) != "" {
		base += " - " + strings.TrimSpace(work.Authors)
	}
	if strings.TrimSpace(base) == "" {
		base = strings.TrimSuffix(asset.Filename, filepath.Ext(asset.Filename))
	}
	base = sanitizeFilenameBase(base)
	if base == "" {
		base = "book"
	}
	ext := forcedExt
	if ext == "" {
		ext = asset.Extension
	}
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return base + strings.ToLower(ext)
}

func sanitizeFilenameBase(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer("/", " ", "\\", " ", ":", " ", "*", " ", "?", " ", "\"", " ", "<", " ", ">", " ", "|", " ")
	s = replacer.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func normalizePreset(preset Preset) Preset {
	preset = Preset(strings.ToLower(strings.TrimSpace(string(preset))))
	if ValidPreset(preset) {
		return preset
	}
	return PresetGeneric
}

func normalizePlanOptions(opts PlanOptions) PlanOptions {
	opts.Preset = normalizePreset(opts.Preset)
	opts.RequestedAssetID = strings.TrimSpace(opts.RequestedAssetID)
	opts.RequestedTarget = converter.NormalizeTarget(string(opts.RequestedTarget))
	if opts.AttachmentLimitMB <= 0 {
		opts.AttachmentLimitMB = DefaultAttachmentLimitMB
	}
	return opts
}

func FormatBytesMB(bytes int64) string {
	if bytes <= 0 {
		return "0 MB"
	}
	mb := float64(bytes) / (1024 * 1024)
	return fmt.Sprintf("%.1f MB", mb)
}
