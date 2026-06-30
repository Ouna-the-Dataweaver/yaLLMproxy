package server

import (
	"context"
	"net/http"
)

type requiredHeaderAttributionContextKey struct{}

func requestWithRequiredHeaderAttribution(r *http.Request, values map[string]string) *http.Request {
	if r == nil || len(values) == 0 {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), requiredHeaderAttributionContextKey{}, cloneStringMap(values)))
}

func requiredHeaderAttribution(r *http.Request) map[string]string {
	if r == nil {
		return nil
	}
	values, _ := r.Context().Value(requiredHeaderAttributionContextKey{}).(map[string]string)
	return cloneStringMap(values)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
