package relume

// The partner API's objects (Relume Partner Protocol v1, section 8). Unknown
// response fields are ignored, as the protocol requires; nullable fields are
// pointers.

import "time"

// Partner API error codes (protocol section 9), in Error.Code.
const (
	CodeInvalidRequest       = "invalid_request"
	CodeInvalidCursor        = "invalid_cursor"
	CodeMissingAPIKey        = "missing_api_key"
	CodeInvalidAPIKey        = "invalid_api_key"
	CodePartnerSuspended     = "partner_suspended"
	CodeUserDeactivated      = "user_deactivated"
	CodeUserSuspended        = "user_suspended"
	CodeNotFound             = "not_found"
	CodeUserNotFound         = "user_not_found"
	CodeSiteNotFound         = "site_not_found"
	CodeSiteHasCustomDomain  = "site_has_custom_domain"
	CodeSiteHasHostingPlan   = "site_has_hosting_plan"
	CodeExportNotFound       = "export_not_found"
	CodeLedgerEntryNotFound  = "ledger_entry_not_found"
	CodePartnerUserUnknown   = "partner_user_unknown"
	CodeRateLimited          = "rate_limited"
	CodeInternalError        = "internal_error"
	CodePartnerEndpointError = "partner_endpoint_error"
)

// Page is one page of a list endpoint.
type Page[T any] struct {
	Data []T
	// Always present. Pass it as After to continue.
	NextCursor string
	HasMore    bool
}

// PageQuery is ?after=&limit= on every list endpoint.
type PageQuery struct {
	// A NextCursor from the same list. "" starts from the beginning.
	After string
	// The page size, 1 to 100; 0 means the API's default, 50. It doesn't cap
	// the total: iterating a List fetches every page.
	Limit int
}

type Session struct {
	// Single use, valid for 5 minutes, bound to this environment's
	// publishable key.
	ClientSecret string    `json:"clientSecret"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type UserStatus string

const (
	UserActive      UserStatus = "active"
	UserDeactivated UserStatus = "deactivated"
	// UserSuspended users can only be reactivated by Relume staff.
	UserSuspended UserStatus = "suspended"
)

type User struct {
	UserID    string     `json:"userId"`
	Status    UserStatus `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	// When the user last opened the builder; nil before their first session.
	LastActiveAt *time.Time `json:"lastActiveAt"`
	// Sites they own, deleted sites excluded.
	SiteCount int `json:"siteCount"`
}

// ExportStatus: new statuses only come with notice.
type ExportStatus string

const (
	ExportQueued  ExportStatus = "queued"
	ExportRunning ExportStatus = "running"
	ExportReady   ExportStatus = "ready"
	ExportFailed  ExportStatus = "failed"
	// ExportExpired: a newer export of the site replaced it, or its owner is
	// deactivated or suspended (ready again once they're reactivated).
	ExportExpired ExportStatus = "expired"
)

// Export is a zip of a site, made with Export in the builder.
type Export struct {
	// What GetExport takes: from the embed's onExport, or a Site's
	// LastExport.
	ExportID string       `json:"exportId"`
	SiteID   string       `json:"siteId"`
	Status   ExportStatus `json:"status"`
	// A signed zip URL, new on each fetch: fetch the export again rather
	// than store it. nil unless Status is ExportReady.
	DownloadURL *string   `json:"downloadUrl"`
	CreatedAt   time.Time `json:"createdAt"`
	// When DownloadURL stops working, 10 minutes after the fetch; nil unless
	// Status is ExportReady.
	ExpiresAt *time.Time `json:"expiresAt"`
}

// PublishStatus: new statuses only come with notice.
type PublishStatus string

const (
	SitePublished   PublishStatus = "published"
	SiteUnpublished PublishStatus = "unpublished"
)

// HostingStatus is the state of the site's hosting plan. New statuses only
// come with notice.
type HostingStatus string

const (
	// HostingActive: in force. A plan canceled at the end of its period stays
	// active until then.
	HostingActive HostingStatus = "active"
	// HostingPastDue: a payment failed and is owed.
	HostingPastDue HostingStatus = "past_due"
	// HostingCanceled: it ended, or lapsed unpaid or paused.
	HostingCanceled HostingStatus = "canceled"
	// HostingNone: the site never had a plan.
	HostingNone HostingStatus = "none"
)

// SitePublish is where and whether a site is published. Publishing, domains
// and hosting plans are managed in the builder; the API only reports them.
type SitePublish struct {
	Status PublishStatus `json:"status"`
	// Where it is live: its custom domain when one is connected, else its
	// preview URL. nil unless Status is SitePublished.
	URL *string `json:"url"`
	// The last publish. It stays when the site is unpublished.
	PublishedAt   time.Time     `json:"publishedAt"`
	HostingStatus HostingStatus `json:"hostingStatus"`
}

type Site struct {
	// The id relume.open({ site }) takes in the embed.
	SiteID string `json:"siteId"`
	// Your own id for the owner.
	UserID    string    `json:"userId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// Includes changes Relume makes itself, such as a new screenshot.
	UpdatedAt time.Time `json:"updatedAt"`
	// Signed, and stops working within 7 days: fetch the site again rather
	// than store it.
	ThumbnailURL *string `json:"thumbnailUrl"`
	// The most recent export, or nil.
	LastExport *Export `json:"lastExport"`
	// nil until the site is first published.
	Publish *SitePublish `json:"publish"`
	// True until someone starts the site's intake (types a description or
	// attaches a file), unlocks a page or starts a chat in it. You can use it
	// to filter sites out of a list.
	IsEmpty bool `json:"isEmpty"`
}

type CreateSiteParams struct {
	// At most 200 characters. "" is the API's default, "Untitled site".
	Name string
}

type CreatedSite struct {
	SiteID string `json:"siteId"`
}

type UpdateSiteParams struct {
	// Trimmed, then 1 to 200 characters.
	Name string
}

type DeletedSite struct {
	SiteID string `json:"siteId"`
	// Always true.
	Deleted bool `json:"deleted"`
}

type LedgerEntryType string

const (
	LedgerCharge   LedgerEntryType = "charge"
	LedgerReversal LedgerEntryType = "reversal"
)

// LedgerEntryStatus: a charge is pending, charged, declined, unknown, void
// or reversed; a reversal is pending or reversed.
type LedgerEntryStatus string

const (
	LedgerPending  LedgerEntryStatus = "pending"
	LedgerCharged  LedgerEntryStatus = "charged"
	LedgerDeclined LedgerEntryStatus = "declined"
	LedgerUnknown  LedgerEntryStatus = "unknown"
	LedgerVoid     LedgerEntryStatus = "void"
	LedgerReversed LedgerEntryStatus = "reversed"
)

type LedgerEntry struct {
	// The chargeId or reversalId sent in the call: upsert on it.
	ID             string            `json:"id"`
	Type           LedgerEntryType   `json:"type"`
	Status         LedgerEntryStatus `json:"status"`
	UserID         string            `json:"userId"`
	PartnerCredits int               `json:"partnerCredits"`
	RelumeCredits  int               `json:"relumeCredits"`
	// nil when it doesn't apply, e.g. on a staff refund. New actions may
	// appear.
	Action *string `json:"action"`
	SiteID *string `json:"siteId"`
	PageID *string `json:"pageId"`
	// On a reversal tied to a charge; otherwise nil.
	ChargeID *string `json:"chargeId"`
	// On a reversal (work_failed, charge_unknown, staff_refund, …); otherwise
	// nil.
	Reason    *string   `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
	// When the entry reached a final status; otherwise nil.
	ResolvedAt *time.Time `json:"resolvedAt"`
}

type PartnerResponse struct {
	// From the last attempt; nil after a timeout or network error.
	HTTPStatus *int `json:"httpStatus"`
	// nil after a timeout or network error.
	Body *struct {
		Balance *int `json:"balance"`
		// A declined charge's reason, as your 402 sent it; its metadata
		// isn't kept. Empty when there was none.
		Reason string `json:"reason"`
	} `json:"body"`
	Attempts int `json:"attempts"`
}

// LedgerEntryDetail is GET /ledger/{id}: the entry and what your endpoint
// last answered.
type LedgerEntryDetail struct {
	LedgerEntry
	// nil when Relume resolved the entry without calling you.
	PartnerResponse *PartnerResponse `json:"partnerResponse"`
}

type UsageGroup string

const (
	UsageByDay    UsageGroup = "day"
	UsageByUser   UsageGroup = "user"
	UsageByAction UsageGroup = "action"
)

type UsageParams struct {
	// YYYY-MM-DD (UTC), inclusive.
	From string
	// YYYY-MM-DD (UTC), exclusive. At most 366 days after From.
	To string
	// One row per combination of these. Empty: one row for the whole range.
	GroupBy []UsageGroup
	After   string
	// The page size, as in PageQuery.
	Limit int
}

type UsageTotals struct {
	Charged  int `json:"charged"`
	Reversed int `json:"reversed"`
	// Charged - Reversed. Relume invoices on RelumeCredits.Net.
	Net int `json:"net"`
}

type UsageRow struct {
	// YYYY-MM-DD; "" unless grouped by day.
	Day string `json:"day"`
	// "" unless grouped by user.
	UserID string `json:"userId"`
	// nil unless grouped by action, and for a staff refund.
	Action         *string     `json:"action"`
	PartnerCredits UsageTotals `json:"partnerCredits"`
	RelumeCredits  UsageTotals `json:"relumeCredits"`
}

type Price struct {
	Action string `json:"action"`
	// RelumeCredits times CreditRate when the action has a rate; otherwise
	// the price Relume staff set, nil until then (Relume refuses the action).
	PartnerCredits *int `json:"partnerCredits"`
	RelumeCredits  int  `json:"relumeCredits"`
	// Your credits per Relume credit for this action: its own rate, else your
	// default rate. nil when the action has the price Relume staff set.
	CreditRate *int `json:"creditRate"`
}

// CreditUnit is what you call your credits: the word the builder shows
// beside amounts.
type CreditUnit struct {
	// e.g. "coin".
	Singular string `json:"singular"`
	// e.g. "coins".
	Plural string `json:"plural"`
	// The https icon the builder shows beside amounts; "" when not set.
	IconURL string `json:"iconUrl,omitempty"`
}

type PriceList struct {
	Data []Price `json:"data"`
	// Your default credits per Relume credit in this environment, set on the
	// Partners page; nil for none. It prices every action without its own
	// rate, including actions added later.
	CreditRate *int `json:"creditRate"`
	// What the builder calls your credits, as Relume staff set it for you.
	CreditUnit CreditUnit `json:"creditUnit"`
}
