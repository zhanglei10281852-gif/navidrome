package events

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode"
)

type eventCtxKey string

const broadcastToAllKey eventCtxKey = "broadcastToAll"

// broadcastToAll is a context key that can be used to broadcast an event to all clients
func broadcastToAll(ctx context.Context) context.Context {
	return context.WithValue(ctx, broadcastToAllKey, true)
}

type Event interface {
	Name(Event) string
	Data(Event) string
}

type baseEvent struct{}

func (e *baseEvent) Name(evt Event) string {
	str := strings.TrimPrefix(reflect.TypeOf(evt).String(), "*events.")
	return str[:0] + string(unicode.ToLower(rune(str[0]))) + str[1:]
}

func (e *baseEvent) Data(evt Event) string {
	data, _ := json.Marshal(evt)
	return string(data)
}

type ScanStatus struct {
	baseEvent
	Scanning    bool          `json:"scanning"`
	Count       int64         `json:"count"`
	FolderCount int64         `json:"folderCount"`
	Error       string        `json:"error"`
	ScanType    string        `json:"scanType"`
	ElapsedTime time.Duration `json:"elapsedTime"`
}

// ScanPlanStatus reports progress of a scan-plan preview or apply. It is distinct
// from ScanStatus so plan work never shows up as a regular library scan.
type ScanPlanStatus struct {
	baseEvent
	PlanID      string `json:"planId"`
	Mode        string `json:"mode"` // "preview" | "apply"
	Phase       string `json:"phase"`
	Stage       string `json:"stage"`
	LibID       int    `json:"libId"`
	Count       int64  `json:"count"`
	FolderCount int64  `json:"folderCount"`
	Path        string `json:"path"`
	Warning     string `json:"warning,omitempty"`
	Error       string `json:"error,omitempty"`
	Done        bool   `json:"done,omitempty"`
}

type KeepAlive struct {
	baseEvent
	TS int64 `json:"ts"`
}

type ServerStart struct {
	baseEvent
	StartTime time.Time `json:"startTime"`
	Version   string    `json:"version"`
}

const Any = "*"

type RefreshResource struct {
	baseEvent
	resources map[string][]string
}

type NowPlayingCount struct {
	baseEvent
	Count int `json:"count"`
}

func (rr *RefreshResource) With(resource string, ids ...string) *RefreshResource {
	if rr.resources == nil {
		rr.resources = make(map[string][]string)
	}
	if len(ids) == 0 {
		rr.resources[resource] = append(rr.resources[resource], Any)
	}
	rr.resources[resource] = append(rr.resources[resource], ids...)
	return rr
}

func (rr *RefreshResource) Data(evt Event) string {
	if rr.resources == nil {
		return `{"*":"*"}`
	}
	r := evt.(*RefreshResource)
	data, _ := json.Marshal(r.resources)
	return string(data)
}
