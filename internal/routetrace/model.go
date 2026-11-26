// Package routetrace 保存单个入站请求的有限选路事实，不参与业务选路。
package routetrace

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

type OwnerType string

const (
	OwnerFull               OwnerType = "full"
	OwnerAccessKey          OwnerType = "access_key"
	OwnerWorkspaceInference OwnerType = "workspace_inference"
)

// Subject 必须已由当前配置或宿主鉴权。Credential 只在计算归属时使用，不保存在 Store。
type Subject struct {
	Type       OwnerType
	Credential string
}

type Request struct {
	Model     *string `json:"model"`
	RouteKind *string `json:"route_kind"`
	Stream    *bool   `json:"stream"`
	Workspace *string `json:"workspace"`
}

type Target struct {
	ModelID         *string `json:"model_id"`
	ProviderID      *string `json:"provider_id"`
	KeyName         *string `json:"key_name"`
	UpstreamModelID *string `json:"upstream_model_id"`
}

type Event struct {
	Seq            int     `json:"seq"`
	OffsetMS       int64   `json:"offset_ms"`
	Kind           string  `json:"kind"`
	Stage          *string `json:"stage,omitempty"`
	Reason         *string `json:"reason,omitempty"`
	Mode           *string `json:"mode,omitempty"`
	Protocol       *string `json:"protocol,omitempty"`
	Result         *string `json:"result,omitempty"`
	Target         *Target `json:"target,omitempty"`
	FromTarget     *Target `json:"from,omitempty"`
	ToTarget       *Target `json:"to,omitempty"`
	AttemptNo      *int    `json:"attempt_no"`
	UpstreamStatus *int    `json:"upstream_status"`
	Sent           *bool   `json:"sent,omitempty"`
	DurationMS     *int64  `json:"duration_ms"`
	HTTPStatus     *int    `json:"http_status,omitempty"`
	StreamEnd      *string `json:"stream_end,omitempty"`
}

type Final struct {
	HTTPStatus     *int    `json:"http_status"`
	UpstreamStatus *int    `json:"upstream_status"`
	Target         *Target `json:"target"`
	StreamEnd      *string `json:"stream_end"`
}

type document struct {
	SchemaVersion int        `json:"schema_version"`
	RequestID     string     `json:"request_id"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	DurationMS    *int64     `json:"duration_ms"`
	Status        string     `json:"status"`
	Outcome       *string    `json:"outcome"`
	Request       struct {
		Request
		CallerType string `json:"caller_type"`
	} `json:"request"`
	Events        []Event `json:"events"`
	Final         Final   `json:"final"`
	Truncated     bool    `json:"truncated"`
	OmittedEvents int     `json:"omitted_events"`
}

// restrictedDocument 是单独的白名单；增加管理字段不会扩大受限投影。
type restrictedDocument struct {
	SchemaVersion int        `json:"schema_version"`
	RequestID     string     `json:"request_id"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	Status        string     `json:"status"`
	Outcome       *string    `json:"outcome"`
	Request       struct {
		Model  *string `json:"model"`
		Stream *bool   `json:"stream"`
	} `json:"request"`
	Final struct {
		HTTPStatus *int    `json:"http_status"`
		StreamEnd  *string `json:"stream_end"`
	} `json:"final"`
	Truncated bool `json:"truncated"`
}

func (d document) project(full bool) (json.RawMessage, error) {
	if full {
		return json.Marshal(d)
	}
	view := restrictedDocument{
		SchemaVersion: d.SchemaVersion, RequestID: d.RequestID, StartedAt: d.StartedAt,
		FinishedAt: d.FinishedAt, Status: d.Status, Outcome: d.Outcome, Truncated: d.Truncated,
	}
	view.Request.Model, view.Request.Stream = d.Request.Model, d.Request.Stream
	view.Final.HTTPStatus, view.Final.StreamEnd = d.Final.HTTPStatus, d.Final.StreamEnd
	return json.Marshal(view)
}

// Label 只用于来源已确认的配置标签；不接受原始请求值作为兜底。
func Label(value string, knownCredentials ...string) *string {
	if value == "" || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return nil
	}
	limit := len(value)
	if limit > 128 {
		limit = 128
		for !utf8.RuneStart(value[limit]) {
			limit--
		}
	}
	for _, key := range knownCredentials {
		if value == key || value[:limit] == key {
			return nil
		}
	}
	return ptr(strings.Clone(value[:limit]))
}

// ConfigLabel 对快照中的已知入站与上游凭据再做一次检查，不将配置或凭据传给 collector。
func ConfigLabel(cfg *config.RouterConfig, value string) *string {
	label := Label(value)
	if cfg == nil || label == nil {
		return nil
	}
	matches := func(secret string) bool { return value == secret || *label == secret }
	if matches(cfg.LocalAPIKey) {
		return nil
	}
	for _, key := range cfg.AccessKeys {
		if matches(key.Key) {
			return nil
		}
	}
	for _, space := range cfg.Workspaces {
		if matches(space.APIKey) || matches(space.InferenceKey) {
			return nil
		}
	}
	for _, provider := range cfg.Providers {
		for _, key := range provider.Keys {
			if matches(key.APIKey) {
				return nil
			}
		}
	}
	for _, model := range cfg.Models {
		for _, key := range model.Keys {
			if matches(key.APIKey) {
				return nil
			}
		}
	}
	return label
}

func ptr[T any](value T) *T { return &value }

func copyPtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return ptr(*value)
}

func copyLabel(value *string) *string {
	if value == nil {
		return nil
	}
	return Label(*value)
}

func enum(value *string, choices ...string) *string {
	if value != nil {
		for _, choice := range choices {
			if *value == choice {
				return ptr(choice)
			}
		}
	}
	return nil
}

func status(value *int) *int {
	if value != nil && *value >= 100 && *value <= 599 {
		return ptr(*value)
	}
	return nil
}

func copyTarget(t *Target) *Target {
	if t == nil {
		return nil
	}
	return &Target{copyLabel(t.ModelID), copyLabel(t.ProviderID), copyLabel(t.KeyName), copyLabel(t.UpstreamModelID)}
}

func (e Event) safe() (Event, bool) {
	kind := enum(&e.Kind, "resolve", "candidate", "selection", "probe", "attempt", "fallback", "finish")
	if kind == nil {
		return Event{}, false
	}
	safe := Event{Kind: *kind, Target: copyTarget(e.Target)}
	safe.Stage = enum(e.Stage, "request", "primary", "fallback", "cooldown_soft_fallback", "specified", "only_first", "prepare", "primary/preflight", "fallback/preflight", "primary/specified", "fallback/specified", "primary/regular", "fallback/regular", "primary/cooldown_soft_fallback", "fallback/cooldown_soft_fallback")
	switch safe.Kind {
	case "resolve":
		safe.Reason = enum(e.Reason, "direct", "alias", "task", "unified", "not_found", "model_not_allowed", "missing_model", "malformed_input", "local_rejection")
		safe.Protocol = protocol(e.Protocol)
	case "candidate":
		safe.Reason = enum(e.Reason, "disabled", "provider_not_allowed", "quota_frozen", "excluded_after_attempt", "cooldown", "balance_limited", "concurrency_limited", "eligible_not_selected", "selected")
	case "selection":
		safe.Mode = enum(e.Mode, "specified", "only_first", "priority", "round_robin")
		safe.Reason = enum(e.Reason, "selected", "sticky", "lowest_active_round_robin", "cooldown_soft_fallback", "unavailable", "not_found", "quota_frozen", "balance_limited", "concurrency_limited", "cooldown")
	case "probe":
		safe.Reason = enum(e.Reason, "supported", "unsupported", "error", "not_sent")
		safe.UpstreamStatus = status(e.UpstreamStatus)
	case "attempt":
		safe.Protocol = protocol(e.Protocol)
		safe.Result = enum(e.Result, "response", "transport_error", "not_sent")
		safe.Reason = attemptReason(e.Reason)
		safe.Sent = copyPtr(e.Sent)
		if safe.Sent != nil && *safe.Sent && e.AttemptNo != nil && *e.AttemptNo > 0 {
			safe.AttemptNo = copyPtr(e.AttemptNo)
		}
		safe.UpstreamStatus = status(e.UpstreamStatus)
		if e.DurationMS != nil && *e.DurationMS >= 0 {
			safe.DurationMS = copyPtr(e.DurationMS)
		}
	case "fallback":
		safe.Result = enum(e.Result, "native_to_compatible", "model_fallback")
		safe.Reason = enum(e.Reason, "native_to_compatible", "model_fallback")
		if safe.Reason == nil {
			safe.Reason = attemptReason(e.Reason)
		}
		safe.FromTarget, safe.ToTarget = copyTarget(e.FromTarget), copyTarget(e.ToTarget)
	case "finish":
		safe.HTTPStatus = status(e.HTTPStatus)
		safe.StreamEnd = enum(e.StreamEnd, streamEnds...)
		safe.Reason = enum(e.Reason, streamEnds...)
	}
	return safe, true
}

var streamEnds = []string{"complete", "upstream_error", "incomplete", "client_canceled", "not_stream"}

func protocol(value *string) *string {
	return enum(value, "native", "compatible", "openai", "anthropic", "claude", "gemini", "chat/completions", "responses", "messages", "image", "images/generations", "images/edits", "embeddings", "speech", "audio/speech", "transcriptions", "audio/transcriptions", "video", "videos", "rerank", "chat_completions", "gemini/generateContent", "gemini/streamGenerateContent")
}

func attemptReason(value *string) *string {
	return enum(value, "retryable_http", "transport_error", "first_byte_timeout", "client_canceled", "budget_exhausted", "unsupported", "not_sent", "upstream_error", "no_available_key", "retryable", "endpoint_not_supported", "request_build_failed", "connection_error", "sent", "response", "incomplete", "local_rejection")
}
