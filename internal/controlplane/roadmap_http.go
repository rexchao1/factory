package controlplane

import "net/http"

// getRoadmap serves the planning state the orchestrator keeps on disk, joined
// against the factory's own runs so the page can say which pebble is being
// built right now. It is read-only in the strongest sense: the handler opens
// files and reads work rows, and there is no sibling route that writes one.
// Planning stays the orchestrator's job, and the cockpit only shows it.
func (a *API) getRoadmap(w http.ResponseWriter, r *http.Request) {
	roadmap, err := readRoadmap(a.roadmapRoot)
	if err != nil {
		a.logger.Error("read roadmap", "error", err)
		writeError(w, &ServiceError{Code: "roadmap_unreadable", Message: "the roadmap directory could not be read", Status: 500, Err: err})
		return
	}
	roadmapApplyWork(&roadmap, roadmapWorkIndex(r.Context(), a.store))
	roadmapSortWaiting(roadmap.Waiting)
	writeJSON(w, http.StatusOK, roadmap)
}
