package delivery

import (
	"testing"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/format"
)

func TestPlanDeliveryKindleDirectPreference(t *testing.T) {
	book := Book{
		Title:   "The Book",
		Authors: "A. Writer",
		Assets: []Asset{
			{ID: 1, Filename: "book.pdf", Extension: ".pdf", Format: format.FormatPDF, Size: 1024, IsPrimary: true},
			{ID: 2, Filename: "book.epub", Extension: ".epub", Format: format.FormatEPUB, Size: 2048},
		},
	}

	plan := PlanDelivery(book, PlanOptions{Preset: PresetKindle, AttachmentLimitMB: 25})
	if !plan.Sendable() {
		t.Fatalf("plan not sendable: %+v", plan)
	}
	if plan.AssetID != 2 || plan.Target != "" || plan.Filename != "The Book - A. Writer.epub" {
		t.Fatalf("plan = %+v, want native EPUB despite primary PDF", plan)
	}
}

func TestPlanChoicesUsePolicyOrderAndIncludeAlternates(t *testing.T) {
	book := Book{
		Title: "Choices",
		Assets: []Asset{
			{ID: 1, Filename: "choices.pdf", Extension: ".pdf", Format: format.FormatPDF, Size: 1024, IsPrimary: true},
			{ID: 2, Filename: "choices.fb2.zip", Extension: ".fb2.zip", Format: format.FormatFB2, Size: 1024},
			{ID: 3, Filename: "choices.epub", Extension: ".epub", Format: format.FormatEPUB, Size: 1024},
		},
	}

	choices := PlanChoices(book, PlanOptions{Preset: PresetKindle, AttachmentLimitMB: 25})
	if len(choices) != 3 {
		t.Fatalf("choices = %+v, want EPUB, PDF, FB2->EPUB", choices)
	}
	if !choices[0].Default || choices[0].Plan.AssetID != 3 || choices[0].Plan.Target != "" {
		t.Fatalf("default choice = %+v, want native EPUB", choices[0])
	}
	if choices[1].Plan.AssetID != 1 || choices[1].Plan.Target != "" {
		t.Fatalf("second choice = %+v, want native PDF", choices[1])
	}
	if choices[2].Plan.AssetID != 2 || choices[2].Plan.Target != converter.TargetEPUB {
		t.Fatalf("third choice = %+v, want FB2 -> EPUB", choices[2])
	}
}

func TestPlanDeliveryKindleConvertsLegacyKindleSourcesToEPUB(t *testing.T) {
	for _, tt := range []struct {
		id     int64
		ext    string
		format format.Format
	}{
		{id: 1, ext: ".mobi", format: format.FormatMOBI},
		{id: 2, ext: ".pdb", format: format.FormatPDB},
	} {
		t.Run(format.FormatLabel(tt.format), func(t *testing.T) {
			book := Book{Title: "Old", Assets: []Asset{
				{ID: tt.id, Filename: "old" + tt.ext, Extension: tt.ext, Format: tt.format, Size: 1024},
			}}

			plan := PlanDelivery(book, PlanOptions{Preset: PresetKindle})
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
	book := Book{Title: "FB2", Assets: []Asset{
		{ID: 1, Filename: "fb2.fb2", Extension: ".fb2", Format: format.FormatFB2, Size: 1024, IsPrimary: true},
	}}

	plan := PlanDelivery(book, PlanOptions{Preset: PresetKindle})
	if !plan.Sendable() {
		t.Fatalf("plan not sendable: %+v", plan)
	}
	if plan.AssetID != 1 || plan.Target != converter.TargetEPUB || !plan.Converted || plan.Filename != "FB2.epub" {
		t.Fatalf("plan = %+v, want FB2 -> EPUB", plan)
	}
}

func TestPlanDeliveryKindleAllowsAZW4ToPDF(t *testing.T) {
	book := Book{Title: "Fixed", Assets: []Asset{
		{ID: 1, Filename: "fixed.azw4", Extension: ".azw4", Format: format.FormatAZW4, Size: 1024},
	}}

	plan := PlanDelivery(book, PlanOptions{Preset: PresetKindle})
	if !plan.Sendable() {
		t.Fatalf("plan not sendable: %+v", plan)
	}
	if plan.Target != converter.TargetPDF || plan.Filename != "Fixed.pdf" {
		t.Fatalf("plan = %+v, want AZW4 -> PDF", plan)
	}
}

func TestPlanDeliverySizeLimitUsesEncodedSize(t *testing.T) {
	raw19MB := int64(19 * 1024 * 1024)
	book := Book{Title: "Large", Assets: []Asset{
		{ID: 1, Filename: "large.epub", Extension: ".epub", Format: format.FormatEPUB, Size: raw19MB},
	}}

	plan := PlanDelivery(book, PlanOptions{Preset: PresetKindle, AttachmentLimitMB: 25})
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
	book := Book{Title: "Archive", Assets: []Asset{
		{ID: 1, Filename: "archive.chm", Extension: ".chm", Format: format.FormatCHM, Size: 1024},
	}}

	plan := PlanDelivery(book, PlanOptions{Preset: PresetGeneric})
	if plan.Sendable() || plan.Reason.Code != ReasonNoCompatibleFormat {
		t.Fatalf("generic auto plan = %+v, want explicit choice required", plan)
	}

	explicit := PlanDelivery(book, PlanOptions{Preset: PresetGeneric, RequestedAssetID: 1})
	if !explicit.Sendable() || explicit.AssetID != 1 {
		t.Fatalf("explicit generic plan = %+v, want CHM native", explicit)
	}

	choices := PlanChoices(book, PlanOptions{Preset: PresetGeneric})
	if len(choices) != 1 || choices[0].Default || choices[0].Plan.AssetID != 1 {
		t.Fatalf("generic choices = %+v, want explicit CHM choice", choices)
	}
}

func TestPlanDeliveryPocketBookDirectPreference(t *testing.T) {
	book := Book{
		Title: "Pocket",
		Assets: []Asset{
			{ID: 1, Filename: "pocket.pdf", Extension: ".pdf", Format: format.FormatPDF, Size: 1024, IsPrimary: true},
			{ID: 2, Filename: "pocket.fb2", Extension: ".fb2", Format: format.FormatFB2, Size: 1024},
		},
	}

	plan := PlanDelivery(book, PlanOptions{Preset: PresetPocketBook})
	if !plan.Sendable() || plan.AssetID != 2 || plan.Target != "" {
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
