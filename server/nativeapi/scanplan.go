package nativeapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Masterminds/squirrel"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/scanner"
	"github.com/navidrome/navidrome/server"
)

// scanPlanResponse is the plan payload plus cheap readiness flags for the UI.
type scanPlanResponse struct {
	*model.ScanPlan
	Expired      bool                    `json:"expired"`
	StaleReasons []model.PlanStaleReason `json:"staleReasons,omitempty"`
}

type createPlanRequest struct {
	FullScan bool                `json:"fullScan"`
	Targets  []model.ScanTarget  `json:"targets"`
}

func (api *Router) addScanPlanRoutes(r chi.Router) {
	r.Route("/scanplan", func(r chi.Router) {
		r.Post("/", api.createPlan)
		r.Get("/", api.listPlans)

		r.Route("/{id}", func(r chi.Router) {
			r.Use(server.URLParamsMiddleware)
			r.Get("/", api.getPlan)
			r.Delete("/", api.deletePlan)
			r.Post("/apply", api.applyPlan)
			r.Get("/items", api.listPlanItems)
		})
	})
}

func (api *Router) createPlan(w http.ResponseWriter, r *http.Request) {
	var req createPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	id, err := api.planner.PreviewPlan(r.Context(), model.PlanRequest{
		FullScan: req.FullScan,
		Targets:  req.Targets,
	})
	if err != nil {
		log.Error(r.Context(), "Error creating scan plan", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": model.ScanPlanStatusRunning})
}

func (api *Router) listPlans(w http.ResponseWriter, r *http.Request) {
	opts := model.QueryOptions{Sort: "created_at", Order: "desc"}
	opts.Max, opts.Offset = parsePagination(r)

	plans, err := api.ds.ScanPlan(r.Context()).GetAll(opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	count, err := api.ds.ScanPlan(r.Context()).CountAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total-Count", strconv.FormatInt(count, 10))
	_ = json.NewEncoder(w).Encode(plans)
}

func (api *Router) getPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plan, err := api.ds.ScanPlan(r.Context()).Get(id)
	if err != nil {
		api.writePlanError(w, r, err)
		return
	}
	expired, stale, err := scanner.PlanReadiness(r.Context(), api.ds, plan)
	if err != nil {
		log.Warn(r.Context(), "Error computing plan readiness", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(scanPlanResponse{ScanPlan: plan, Expired: expired, StaleReasons: stale})
}

func (api *Router) deletePlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := api.ds.ScanPlan(r.Context()).Delete(id); err != nil {
		api.writePlanError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (api *Router) applyPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := api.planner.ApplyPlan(r.Context(), id)
	if err != nil {
		api.writePlanError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": model.ScanPlanStatusApplying})
}

func (api *Router) listPlanItems(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := api.ds.ScanPlan(r.Context()).Get(id); err != nil {
		api.writePlanError(w, r, err)
		return
	}

	filters := squirrel.And{}
	if ct := r.URL.Query().Get("changeType"); ct != "" {
		filters = append(filters, squirrel.Eq{"change_type": ct})
	}
	if lib := r.URL.Query().Get("libraryId"); lib != "" {
		if libID, err := strconv.Atoi(lib); err == nil {
			filters = append(filters, squirrel.Eq{"library_id": libID})
		}
	}
	opts := model.QueryOptions{Sort: "created_at", Order: "asc", Filters: filters}
	opts.Max, opts.Offset = parsePagination(r)

	items, err := api.ds.ScanPlan(r.Context()).GetItems(id, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	count, err := api.ds.ScanPlan(r.Context()).CountItems(id, model.QueryOptions{Filters: filters})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total-Count", strconv.FormatInt(count, 10))
	_ = json.NewEncoder(w).Encode(items)
}

// writePlanError maps plan service errors onto HTTP status codes.
func (api *Router) writePlanError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, model.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var stale *scanner.PlanApplyError
	if errors.As(err, &stale) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":        err.Error(),
			"staleReasons": stale.StaleReasons,
		})
		return
	}
	switch {
	case errors.Is(err, scanner.ErrPlanExpired):
		http.Error(w, err.Error(), http.StatusGone)
	case errors.Is(err, scanner.ErrPlanApplying),
		errors.Is(err, scanner.ErrPlanRunning),
		errors.Is(err, scanner.ErrPlanFailed),
		errors.Is(err, scanner.ErrAlreadyScanning):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		log.Error(r.Context(), "Scan plan API error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// parsePagination reads react-admin style _start/_end params (falling back to
// start/end), returning max rows and offset.
func parsePagination(r *http.Request) (int, int) {
	start, _ := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("_start"), r.URL.Query().Get("start")))
	end, _ := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("_end"), r.URL.Query().Get("end")))
	if start < 0 {
		start = 0
	}
	if end > start {
		return end - start, start
	}
	if r.URL.Query().Get("_start") == "" && r.URL.Query().Get("start") == "" {
		return 100, 0
	}
	return 0, start
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
