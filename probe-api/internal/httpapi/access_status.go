package httpapi

import (
	"net/http"
	"net/netip"

	"probe-api/internal/access"
	"probe-api/internal/httpapi/respond"
)

type managementAccessStatus struct {
	SourceIP string `json:"source_ip"`
	Allowed  bool   `json:"allowed"`
}

func registerManagementAccessStatusRoute(mux *http.ServeMux, allowlist access.CIDRSet) {
	if mux == nil {
		return
	}
	handler := authNoStoreMiddleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" {
			respond.Error(writer, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
			return
		}
		if !validFetchSite(request) {
			writeCSRFError(writer, request)
			return
		}
		address, ok := clientIPFromContext(request.Context())
		if !ok || !address.IsValid() || address.Zone() != "" {
			respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
			return
		}
		address = address.Unmap()
		if !managementAddressAllowed(allowlist, address) {
			respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
			return
		}
		respond.JSON(writer, http.StatusOK, managementAccessStatus{SourceIP: address.String(), Allowed: true})
	}))
	mux.HandleFunc("/api/v1/auth/access", exactMethod(http.MethodGet, handler.ServeHTTP))
}

func managementAddressAllowed(allowlist access.CIDRSet, address netip.Addr) bool {
	return !allowlist.Empty() && allowlist.Contains(address)
}
