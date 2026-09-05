package controlplane

import "net/http"

// getRoadmap serves the planning state the orchestrator keeps on disk. It is
// read-only in the strongest sense: the handler opens files, and there is no
// sibling route that writes one. Planning stays the orchestrator's job, and
// the cockpit only shows it.
func (a *API) getRoadmap(w http.ResponseWriter, r *http.Request) {
	roadmap, err := readRoadmap(a.roadmapRoot)
	if err != nil {
		a.logger.Error("read roadmap", "error", err)
		writeError(w, &ServiceError{Code: "roadmap_unreadable", Message: "the roadmap directory could not be read", Status: 500, Err: err})
		return
	}
	writeJSON(w, http.StatusOK, roadmap)
}
