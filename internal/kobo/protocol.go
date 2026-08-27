// Package kobo contains Polka's small, independent Kobo wire adapter. It has no
// database or HTTP dependencies: callers map domain rows in at one boundary and
// receive DTOs that can be encoded directly as JSON.
package kobo

import (
	"crypto/sha1"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"
)

const (
	importedCategoryID  = "00000000-0000-0000-0000-000000000001"
	maxDescriptionBytes = 64 << 10
)

var seriesUUIDNamespace = uuid.MustParse("e528a6d9-824d-4d47-a7e4-cbf4c58b2159")

type Publication struct {
	AssetID       string
	WorkID        string
	Size          int64
	Title         string
	Description   string
	Publisher     string
	PublishedDate string
	Language      string
	Series        string
	SeriesIndex   *float64
	Authors       []string
	AddedAt       int64
	ModifiedAt    int64
}

type Change struct {
	Publication
	Revision      int64
	FirstRevision int64
	Present       bool
	ChangedAt     int64
}

type ActivePeriod struct {
	From string `json:"From"`
}

type Entitlement struct {
	Accessibility       string       `json:"Accessibility"`
	ActivePeriod        ActivePeriod `json:"ActivePeriod"`
	Created             string       `json:"Created"`
	CrossRevisionID     string       `json:"CrossRevisionId"`
	ID                  string       `json:"Id"`
	IsRemoved           bool         `json:"IsRemoved"`
	IsHiddenFromArchive bool         `json:"IsHiddenFromArchive"`
	IsLocked            bool         `json:"IsLocked"`
	LastModified        string       `json:"LastModified"`
	OriginCategory      string       `json:"OriginCategory"`
	RevisionID          string       `json:"RevisionId"`
	Status              string       `json:"Status"`
}

type Money struct {
	CurrencyCode string  `json:"CurrencyCode,omitempty"`
	TotalAmount  float64 `json:"TotalAmount"`
}

type DownloadURL struct {
	Format   string `json:"Format"`
	Size     int64  `json:"Size"`
	URL      string `json:"Url"`
	Platform string `json:"Platform"`
}

type Publisher struct {
	Imprint string `json:"Imprint"`
	Name    string `json:"Name"`
}

type ContributorRole struct {
	Name string `json:"Name"`
}

type Series struct {
	ID          string  `json:"Id"`
	Name        string  `json:"Name"`
	Number      float64 `json:"Number"`
	NumberFloat float64 `json:"NumberFloat"`
}

type Metadata struct {
	Categories              []string          `json:"Categories"`
	CoverImageID            string            `json:"CoverImageId"`
	CrossRevisionID         string            `json:"CrossRevisionId"`
	CurrentDisplayPrice     Money             `json:"CurrentDisplayPrice"`
	CurrentLoveDisplayPrice Money             `json:"CurrentLoveDisplayPrice"`
	Description             string            `json:"Description"`
	DownloadURLs            []DownloadURL     `json:"DownloadUrls"`
	EntitlementID           string            `json:"EntitlementId"`
	ExternalIDs             []string          `json:"ExternalIds"`
	Genre                   string            `json:"Genre"`
	IsEligibleForKoboLove   bool              `json:"IsEligibleForKoboLove"`
	IsInternetArchive       bool              `json:"IsInternetArchive"`
	IsPreOrder              bool              `json:"IsPreOrder"`
	IsSocialEnabled         bool              `json:"IsSocialEnabled"`
	Language                string            `json:"Language"`
	PhoneticPronunciations  map[string]string `json:"PhoneticPronunciations"`
	PublicationDate         string            `json:"PublicationDate"`
	Publisher               Publisher         `json:"Publisher"`
	RevisionID              string            `json:"RevisionId"`
	Title                   string            `json:"Title"`
	WorkID                  string            `json:"WorkId"`
	Contributors            []string          `json:"Contributors"`
	ContributorRoles        []ContributorRole `json:"ContributorRoles"`
	Series                  *Series           `json:"Series,omitzero"`
}

type EntitlementPayload struct {
	BookEntitlement Entitlement `json:"BookEntitlement"`
	BookMetadata    *Metadata   `json:"BookMetadata,omitzero"`
}

type SyncItem struct {
	NewEntitlement     *EntitlementPayload `json:"NewEntitlement,omitzero"`
	ChangedEntitlement *EntitlementPayload `json:"ChangedEntitlement,omitzero"`
}

func BuildSyncItem(change Change, afterRevision int64, baseURL string) SyncItem {
	payload := &EntitlementPayload{BookEntitlement: BuildEntitlement(change)}
	if change.Present {
		metadata := BuildMetadata(change.Publication, baseURL)
		payload.BookMetadata = &metadata
	}
	// New/changed is a property of what this client has acknowledged, not of
	// the compacted row's latest revision. A fresh or reset device must receive
	// NewEntitlement even when metadata changed before its first sync.
	if change.Present && afterRevision < change.FirstRevision {
		return SyncItem{NewEntitlement: payload}
	}
	return SyncItem{ChangedEntitlement: payload}
}

func BuildEntitlement(change Change) Entitlement {
	modifiedAt := max(change.ChangedAt, change.ModifiedAt)
	return Entitlement{
		Accessibility:       "Full",
		ActivePeriod:        ActivePeriod{From: timestamp(change.AddedAt)},
		Created:             timestamp(change.AddedAt),
		CrossRevisionID:     change.AssetID,
		ID:                  change.AssetID,
		IsRemoved:           !change.Present,
		IsHiddenFromArchive: false,
		IsLocked:            false,
		LastModified:        timestamp(modifiedAt),
		OriginCategory:      "Imported",
		RevisionID:          change.AssetID,
		Status:              "Active",
	}
}

func BuildMetadata(publication Publication, baseURL string) Metadata {
	language := strings.TrimSpace(publication.Language)
	if language == "" {
		language = "en"
	}
	contributors := append([]string(nil), publication.Authors...)
	roles := make([]ContributorRole, 0, len(contributors))
	for _, author := range contributors {
		roles = append(roles, ContributorRole{Name: author})
	}
	metadata := Metadata{
		Categories:              []string{importedCategoryID},
		CoverImageID:            publication.AssetID,
		CrossRevisionID:         publication.AssetID,
		CurrentDisplayPrice:     Money{CurrencyCode: "USD", TotalAmount: 0},
		CurrentLoveDisplayPrice: Money{TotalAmount: 0},
		Description:             boundedDescription(publication.Description),
		DownloadURLs: []DownloadURL{{
			Format:   "KEPUB",
			Size:     publication.Size,
			URL:      strings.TrimRight(baseURL, "/") + "/download/" + url.PathEscape(publication.AssetID) + "/kepub",
			Platform: "Generic",
		}},
		EntitlementID:          publication.AssetID,
		ExternalIDs:            []string{},
		Genre:                  importedCategoryID,
		IsEligibleForKoboLove:  false,
		IsInternetArchive:      false,
		IsPreOrder:             false,
		IsSocialEnabled:        true,
		Language:               language,
		PhoneticPronunciations: map[string]string{},
		PublicationDate:        publicationTimestamp(publication.PublishedDate, publication.AddedAt),
		Publisher:              Publisher{Imprint: "", Name: publication.Publisher},
		RevisionID:             publication.AssetID,
		Title:                  publication.Title,
		WorkID:                 publication.AssetID,
		Contributors:           contributors,
		ContributorRoles:       roles,
	}
	if publication.Series != "" {
		number := 1.0
		if publication.SeriesIndex != nil {
			number = *publication.SeriesIndex
		}
		metadata.Series = &Series{
			ID:          stableSeriesID(publication.Series),
			Name:        publication.Series,
			Number:      number,
			NumberFloat: number,
		}
	}
	return metadata
}

func boundedDescription(value string) string {
	if len(value) <= maxDescriptionBytes {
		return value
	}
	end := maxDescriptionBytes - len("…")
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "…"
}

func timestamp(unix int64) string {
	if unix <= 0 {
		return "1970-01-01T00:00:00Z"
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func publicationTimestamp(value string, fallback int64) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return timestamp(fallback)
}

// stableSeriesID is a Polka-namespaced UUIDv5-shaped value.
// Kobo treats it as an opaque stable identifier; the namespace is Polka-local.
func stableSeriesID(name string) string {
	h := sha1.New()
	h.Write(seriesUUIDNamespace[:])
	h.Write([]byte(name))
	var id uuid.UUID
	copy(id[:], h.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}
