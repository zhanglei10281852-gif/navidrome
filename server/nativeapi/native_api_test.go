package nativeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deluan/rest"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core"
)

// fakeShare implements only Rotate of core.Share for the rotate handler; the
// rest of the interface is satisfied by the embedded nil interface and never
// exercised by this handler.
type fakeShare struct {
	core.Share
	rotateErr error
	epoch     int
	calledWith string
}

func (f *fakeShare) Rotate(_ context.Context, id string) (int, error) {
	f.calledWith = id
	return f.epoch, f.rotateErr
}

func requestWithID(id string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/share/"+id+"/rotate", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestRotateShare_Success(t *testing.T) {
	svc := &fakeShare{epoch: 3}
	w := httptest.NewRecorder()
	rotateShare(svc)(w, requestWithID("sh1"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.calledWith != "sh1" {
		t.Fatalf("expected rotate called with sh1, got %q", svc.calledWith)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp["epoch"] != float64(3) || resp["id"] != "sh1" {
		t.Fatalf("unexpected response body: %v", resp)
	}
}

func TestRotateShare_NotFound(t *testing.T) {
	svc := &fakeShare{rotateErr: rest.ErrNotFound}
	w := httptest.NewRecorder()
	rotateShare(svc)(w, requestWithID("ghost"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRotateShare_PermissionDenied(t *testing.T) {
	svc := &fakeShare{rotateErr: rest.ErrPermissionDenied}
	w := httptest.NewRecorder()
	rotateShare(svc)(w, requestWithID("sh1"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}
