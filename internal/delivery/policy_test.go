package delivery

import (
	"testing"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/format"
)

func TestPlanDeliveryKindleDirectPreference(t *testing.T) {
	work := Work{
		Title:   "The Book",
		Authors: "A. Writer",
		Assets: []Asset{
			{ID: "pdf", Filename: "book.pdf", Extension: ".pdf", Format: format.FormatPDF, Size: 1024, IsPrimary: true},
			{ID: "epub", Filename: "book.epub", Extension: ".epub", Format: format.FormatEPUB, Size: 2048},
		},
	}

	plan := PlanDelivery(work, PlanOptions{Preset: PresetKindle, AttachmentLimitMB: 25})
	if !plan.Sendable() {
		t.Fatalf("plan not sendable: %+v", plan)
	}
	if plan.AssetID != "epub" || plan.Target != "" || plan.Filename != "The Book - A. Writer.epub" {
		t.Fatalf("plan = %+v, want native EPUB despite primary PDF", plan)
	}
}

func TestPlanChoicesUsePolicyOrderAndIncludeAlternates(t *testing.T) {
	work := Work{
		Title: "Choices",
		Assets: []Asset{
			{ID: "pdf", Filename: "choices.pdf", Extension: ".pdf", Format: format.FormatPDF, Size: 1024, IsPrimary: true},
			{ID: "fb2", Filename: "choices.fb2.zip", Extension: ".fb2.zip", Format: format.FormatFB2, Size: 1024},
			{ID: "epub", Filename: "choices.epub", Extension: ".epub", Format: format.FormatEPUB, Size: 1024},
		},
	}

	choices := PlanChoices(work, PlanOptions{Preset: PresetKindle, AttachmentLimitMB: 25})
	if len(choices) != 3 {
		t.Fatalf("choices = %+v, want EPUB, PDF, FB2->EPUB", choices)
	}
	if !choices[0].Default || choices[0].Plan.AssetID != "epub" || choices[0].Plan.Target != "" {
		t.Fatalf("default choice = %+v, want native EPUB", choices[0])
	}
	if choices[1].Plan.AssetID != "pdf" || choices[1].Plan.Target != "" {
		t.Fatalf("second choice = %+v, want native PDF", choices[1])
	}
	if choices[2].Plan.AssetID != "fb2" || choices[2].Plan.Target != converter.TargetEPUB {
		t.Fatalf("third choice = %+v, want FB2 -> EPUB", choices[2])
	}
}

func TestPlanDeliveryKindleConvertsLegacyKindleSourcesToEPUB(t *testing.T) {
	for _, tt := range []struct {
		id     string
		ext    string
		format format.Format
	}{
		{id: "mobi", ext: ".mobi", format: format.FormatMOBI},
		{id: "pdb", ext: ".pdb", format: format.FormatPDB},
	} {
		t.Run(format.FormatLabel(tt.format), func(t *testing.T) {
			work := Work{Title: "Old", Assets: []Asset{
				{ID: tt.id, Filename: "old" + tt.ext, Extension: tt.ext, Format: tt.format, Size: 1024},
			}}

			plan := PlanDelivery(work, PlanOptions{Preset: PresetKindle})
			if !plan.Sendable() {
				t.Fatalf("plan not sendable: %+v", plan)
			}
			if plan.AssetID != tt.id || plan.Target != converter.TargetEPUB || !plan.Converted || plan.Filename != "Old.epub" {
				t.Fatalf("plan = %+v, want %s -> EPUB", plan, format.FormatLabel(tt.format))
			}
		})
	}
}

func TestPlanDeliveryKindleConvertsFB2ToEPUB(t *testing.T) {
	work := Work{Title: "FB2", Assets: []Asset{
		{ID: "fb2", Filename: "fb2.fb2", Extension: ".fb2", Format: format.FormatFB2, Size: 1024, IsPrimary: true},
	}}

	plan := PlanDelivery(work, PlanOptions{Preset: PresetKindle})
	if !plan.Sendable() {
		t.Fatalf("plan not sendable: %+v", plan)
	}
	if plan.AssetID != "fb2" || plan.Target != converter.TargetEPUB || !plan.Converted || plan.Filename != "FB2.epub" {
		t.Fatalf("plan = %+v, want FB2 -> EPUB", plan)
	}
}

func TestPlanDeliveryKindleAllowsAZW4ToPDF(t *testing.T) {
	work := Work{Title: "Fixed", Assets: []Asset{
		{ID: "azw4", Filename: "fixed.azw4", Extension: ".azw4", Format: format.FormatAZW4, Size: 1024},
	}}

	plan := PlanDelivery(work, PlanOptions{Preset: PresetKindle})
	if !plan.Sendable() {
		t.Fatalf("plan not sendable: %+v", plan)
	}
	if plan.Target != converter.TargetPDF || plan.Filename != "Fixed.pdf" {
		t.Fatalf("plan = %+v, want AZW4 -> PDF", plan)
	}
}

func TestPlanDeliverySizeLimitUsesEncodedSize(t *testing.T) {
	raw19MB := int64(19 * 1024 * 1024)
	work := Work{Title: "Large", Assets: []Asset{
		{ID: "epub", Filename: "large.epub", Extension: ".epub", Format: format.FormatEPUB, Size: raw19MB},
	}}

	plan := PlanDelivery(work, PlanOptions{Preset: PresetKindle, AttachmentLimitMB: 25})
	if plan.Sendable() {
		t.Fatalf("plan = %+v, want encoded size to exceed 25 MB limit", plan)
	}
	if plan.Reason.Code != ReasonTooLarge {
		t.Fatalf("reason = %+v, want too_large", plan.Reason)
	}
}

func TestEffectiveLimitUsesPresetCapAndAdminLimit(t *testing.T) {
	if got, want := EffectiveLimitBytes(PresetKindle, 100), int64(50*1024*1024); got != want {
		t.Fatalf("Kindle effective limit = %d, want %d", got, want)
	}
	if got, want := EffectiveLimitBytes(PresetKindle, 10), int64(10*1024*1024); got != want {
		t.Fatalf("Kindle admin-limited effective limit = %d, want %d", got, want)
	}
	if got, want := EffectiveLimitBytes(PresetGeneric, 100), int64(100*1024*1024); got != want {
		t.Fatalf("generic effective limit = %d, want %d", got, want)
	}
}

func TestEncodedSizeIncludesBase64LineBreaksAndOverhead(t *testing.T) {
	raw := int64(57)
	if got, want := EncodedSize(raw), int64(76+2+EmailMessageOverhead); got != want {
		t.Fatalf("EncodedSize(%d) = %d, want %d", raw, got, want)
	}
}

func TestPlanDeliveryGenericRequiresExplicitAssetForObscureFormats(t *testing.T) {
	work := Work{Title: "Archive", Assets: []Asset{
		{ID: "chm", Filename: "archive.chm", Extension: ".chm", Format: format.FormatCHM, Size: 1024},
	}}

	plan := PlanDelivery(work, PlanOptions{Preset: PresetGeneric})
	if plan.Sendable() || plan.Reason.Code != ReasonNoCompatibleFormat {
		t.Fatalf("generic auto plan = %+v, want explicit choice required", plan)
	}

	explicit := PlanDelivery(work, PlanOptions{Preset: PresetGeneric, RequestedAssetID: "chm"})
	if !explicit.Sendable() || explicit.AssetID != "chm" {
		t.Fatalf("explicit generic plan = %+v, want CHM native", explicit)
	}

	choices := PlanChoices(work, PlanOptions{Preset: PresetGeneric})
	if len(choices) != 1 || choices[0].Default || choices[0].Plan.AssetID != "chm" {
		t.Fatalf("generic choices = %+v, want explicit CHM choice", choices)
	}
}

func TestPlanDeliveryPocketBookDirectPreference(t *testing.T) {
	work := Work{
		Title: "Pocket",
		Assets: []Asset{
			{ID: "pdf", Filename: "pocket.pdf", Extension: ".pdf", Format: format.FormatPDF, Size: 1024, IsPrimary: true},
			{ID: "fb2", Filename: "pocket.fb2", Extension: ".fb2", Format: format.FormatFB2, Size: 1024},
		},
	}

	plan := PlanDelivery(work, PlanOptions{Preset: PresetPocketBook})
	if !plan.Sendable() || plan.AssetID != "fb2" || plan.Target != "" {
		t.Fatalf("PocketBook plan = %+v, want native FB2 before PDF", plan)
	}
}

func TestPresetFromEmail(t *testing.T) {
	tests := map[string]Preset{
		"reader@kindle.com":      PresetKindle,
		"reader@free.kindle.cn":  PresetKindle,
		"reader@pbsync.com":      PresetPocketBook,
		"reader@example.invalid": PresetGeneric,
	}
	for email, want := range tests {
		if got := PresetFromEmail(email); got != want {
			t.Fatalf("PresetFromEmail(%q) = %q, want %q", email, got, want)
		}
	}
}
