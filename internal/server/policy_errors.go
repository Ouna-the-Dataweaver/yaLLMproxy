package server

import (
	"errors"
	"net/http"

	"github.com/theio/yallmp-proxy/internal/policy"
)

func (a *App) policyErrorDetails(err error) (int, string, string) {
	switch {
	case errors.Is(err, policy.ErrMissingRequiredHeader):
		a.recordRequiredHeaderMissing()
		return http.StatusBadRequest, "invalid_request_error", "missing_required_header"
	case errors.Is(err, policy.ErrModelAccessDenied):
		return http.StatusForbidden, "authentication_error", "authentication_error"
	default:
		return http.StatusUnauthorized, "authentication_error", "authentication_error"
	}
}
