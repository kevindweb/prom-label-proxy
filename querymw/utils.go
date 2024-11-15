package querymw

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

func instantFromRequest(next http.Handler, w http.ResponseWriter, r *http.Request) (InstantRequest, error) {
	return InstantRequest{
		next: next,
		w:    w,
	}, nil
}

func requestFromInstant(ctx context.Context, req InstantRequest) (*http.Request, error) {
	return nil, nil
}

func rangeFromRequest(next http.Handler, w http.ResponseWriter, r *http.Request) (RangeRequest, error) {
	return RangeRequest{
		next: next,
		w:    w,
	}, nil
}

func requestFromRange(ctx context.Context, req RangeRequest) (*http.Request, error) {
	return nil, nil
}

func prometheusAPIError(w http.ResponseWriter, errorMessage string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)

	res := map[string]string{
		"status":    "error",
		"errorType": "prom-label-proxy",
		"error":     errorMessage,
	}

	if err := json.NewEncoder(w).Encode(res); err != nil {
		log.Printf("error: Failed to encode json: %v", err)
	}
}
