package filecontext

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const MaxRequestBytes = 128 << 10

type Limits struct {
	MaxTurnBatch      int `json:"maxTurnBatch"`
	MaxFileBytes      int `json:"maxFileBytes"`
	MaxSelections     int `json:"maxSelections"`
	MaxSelectionBytes int `json:"maxSelectionBytes"`
	MaxTotalBytes     int `json:"maxTotalBytes"`
}

type Capability struct {
	Contract          string `json:"contract"`
	Enabled           bool   `json:"enabled"`
	Ready             bool   `json:"ready"`
	BuildVersion      string `json:"buildVersion"`
	CodexAdapter      string `json:"codexAdapter"`
	MessageReferences bool   `json:"messageReferences"`
	Limits            Limits `json:"limits"`
}

func NewHTTPHandler(service *Service, buildVersion, codexAdapter string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/file-context/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Capability{Contract: Contract, Enabled: true, Ready: service != nil && service.source != nil, BuildVersion: buildVersion, CodexAdapter: codexAdapter, MessageReferences: true, Limits: Limits{MaxTurnBatch: MaxTurnBatch, MaxFileBytes: MaxFileBytes, MaxSelections: MaxSelections, MaxSelectionBytes: MaxSelectionBytes, MaxTotalBytes: MaxTotalBytes}})
	})
	mux.HandleFunc("POST /v1/file-context/changes", endpoint(service, func() any { return &ChangesRequest{} }, func(r *http.Request, input any) (any, error) {
		return service.Changes(r.Context(), *(input.(*ChangesRequest)))
	}))
	mux.HandleFunc("POST /v1/file-context/read", endpoint(service, func() any { return &ReadRequest{} }, func(r *http.Request, input any) (any, error) {
		return service.Read(r.Context(), *(input.(*ReadRequest)))
	}))
	mux.HandleFunc("POST /v1/file-context/reference/read", endpoint(service, func() any { return &ReferenceReadRequest{} }, func(r *http.Request, input any) (any, error) {
		return service.ReferenceRead(r.Context(), *(input.(*ReferenceReadRequest)))
	}))
	mux.HandleFunc("POST /v1/file-context/prepare", endpoint(service, func() any { return &PrepareRequest{} }, func(r *http.Request, input any) (any, error) {
		return service.Prepare(r.Context(), *(input.(*PrepareRequest)))
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	})
}

func endpoint(service *Service, allocate func() any, call func(*http.Request, any) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service == nil || service.source == nil {
			writeJSON(w, http.StatusServiceUnavailable, &Error{Code: CodeInternal, Message: "NOT_READY"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		input := allocate()
		if err := decoder.Decode(input); err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			writeJSON(w, status, &Error{Code: CodeInvalidArgument, Message: string(CodeInvalidArgument)})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, &Error{Code: CodeInvalidArgument, Message: string(CodeInvalidArgument)})
			return
		}
		result, err := call(r, input)
		if err != nil {
			status := http.StatusUnprocessableEntity
			if CodeOf(err) == CodeInternal {
				status = http.StatusInternalServerError
			}
			writeJSON(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
