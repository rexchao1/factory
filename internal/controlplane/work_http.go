package controlplane

import (
	"errors"
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) admitWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.AdmitWorkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	response, created, err := a.store.AdmitWork(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	a.logStateChange("work", response.RunID, "admitted")
	writeJSON(w, status, response)
}

func (a *API) answerWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.WorkAnswerRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	answer, err := a.store.AnswerWork(r.Context(), r.PathValue("work_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", answer.WorkID, "answered")
	writeJSON(w, http.StatusOK, answer)
}

func (a *API) approveWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ApproveWorkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	work, err := a.store.ApproveWork(r.Context(), r.PathValue("work_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", work.ID, "approved")
	writeJSON(w, http.StatusOK, work)
}

func (a *API) retryWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	work, err := a.store.Work(r.Context(), r.PathValue("work_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	detail, err := a.store.RetrySession(r.Context(), work.RunID, work.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", work.ID, "queued")
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) replaceWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ReplaceWorkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.WorkID != "" && input.WorkID != r.PathValue("work_id") {
		writeError(w, invalid("work_id_mismatch", "body work_id must match the request path"))
		return
	}
	input.WorkID = r.PathValue("work_id")
	replacement, err := a.store.ReplaceWork(r.Context(), input)
	if err != nil {
		var service *ServiceError
		if errors.As(err, &service) && service.Status < http.StatusInternalServerError {
			writeJSON(w, service.Status, protocol.ErrorBody{Error: protocol.APIError{
				Code: service.Code, Message: service.Message,
				AdmissionResult: protocol.AdmissionRejectedBeforeCommit,
				RequestKey:      input.RequestKey,
			}})
			return
		}
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if replacement.Result == protocol.AdmissionAdmitted {
		status = http.StatusCreated
		a.logStateChange("run", replacement.Run.Run.ID, string(replacement.Run.Run.State))
	}
	writeJSON(w, status, replacement)
}
