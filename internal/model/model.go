package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type Strings []string

func (s Strings) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	b, e := json.Marshal(s)
	return string(b), e
}
func (s *Strings) Scan(v any) error {
	if v == nil {
		*s = nil
		return nil
	}
	switch x := v.(type) {
	case string:
		return json.Unmarshal([]byte(x), s)
	case []byte:
		return json.Unmarshal(x, s)
	}
	return fmt.Errorf("invalid JSON list: %T", v)
}
func (Strings) GormDataType() string { return "json" }
func (Strings) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == "postgres" {
		return "JSONB"
	}
	return "TEXT"
}

type Version struct {
	ID             int64      `json:"-"`
	Version        string     `json:"version"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	Notes          string     `json:"notes"`
	ReleaseTag     string     `json:"release_tag"`
	ReleaseID      string     `json:"release_id"`
	PublishedAt    *time.Time `json:"published_at"`
	MinimumVersion *string    `json:"minimum_version"`
	ForceUpdate    bool       `json:"force_update"`
	CreatedAt      time.Time  `json:"-"`
	UpdatedAt      time.Time  `json:"-"`
	RevokedAt      *time.Time `json:"-"`
	RevokeReason   *string    `json:"-"`
	Artifacts      []Artifact `json:"artifacts" gorm:"foreignKey:VersionID"`
}

func (Version) TableName() string { return "version" }

type Artifact struct {
	ID                int64     `json:"-"`
	VersionID         int64     `json:"-"`
	Platform          string    `json:"platform"`
	TargetPlatforms   Strings   `json:"target_platforms"`
	Architecture      string    `json:"architecture"`
	Kind              string    `json:"kind"`
	Variant           string    `json:"variant"`
	DisplayName       string    `json:"display_name"`
	SortOrder         int       `json:"sort_order"`
	IsPublic          bool      `json:"is_public"`
	Filename          string    `json:"filename"`
	RelativePath      string    `json:"relative_path"`
	Size              int64     `json:"size"`
	SHA256            string    `json:"sha256" gorm:"column:sha256"`
	Signature         *string   `json:"signature"`
	SignatureFilename *string   `json:"signature_filename"`
	ContentType       string    `json:"content_type"`
	UploadedAt        time.Time `json:"-"`
	CreatedAt         time.Time `json:"-"`
	UpdatedAt         time.Time `json:"-"`
}

func (Artifact) TableName() string { return "artifact" }

type ChannelPointer struct {
	Channel        string `gorm:"primaryKey"`
	CurrentVersion *string
	UpdatedAt      time.Time
}

func (ChannelPointer) TableName() string { return "channel_pointer" }

type WebhookEvent struct {
	EventID      string `gorm:"primaryKey"`
	Version      *string
	EventType    string
	PayloadHash  string
	ReceivedAt   time.Time
	ProcessedAt  *time.Time
	Status       string
	ErrorMessage *string
}

func (WebhookEvent) TableName() string { return "webhook_event" }

type AuditLog struct {
	ID        int64     `json:"-"`
	Operator  string    `json:"operator"`
	Action    string    `json:"action"`
	Channel   string    `json:"channel"`
	Version   string    `json:"version"`
	Reason    string    `json:"reason"`
	RequestID string    `json:"request_id"`
	IPAddress string    `json:"ip_address"`
	CreatedAt time.Time `json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_log" }

func Ptr[T any](v T) *T { return &v }
