package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"probe-api/internal/access"
	"probe-api/internal/httpapi/respond"
)

type clientIPContextKey struct{}
type peerIPContextKey struct{}
type requestIPLogStateKey struct{}

type requestIPLogState struct {
	peerIP   string
	clientIP string
}

func clientIPMiddleware(trustedProxies access.CIDRSet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		logState, _ := request.Context().Value(requestIPLogStateKey{}).(*requestIPLogState)
		peer := parseRemoteIP(request.RemoteAddr)
		if !peer.IsValid() || peer.Zone() != "" {
			respond.Error(writer, http.StatusBadRequest, "invalid_client_ip", "client address is invalid", requestIDFromContext(request.Context()))
			return
		}
		peer = peer.Unmap()
		if logState != nil {
			logState.peerIP = peer.String()
		}
		client := peer
		forwardedValues := request.Header.Values("X-Probe-Client-IP")
		if len(forwardedValues) != 0 {
			if !trustedProxies.Contains(peer) || len(forwardedValues) != 1 {
				respond.Error(writer, http.StatusBadRequest, "invalid_client_ip", "trusted client IP header is invalid for this connection", requestIDFromContext(request.Context()))
				return
			}
			value := forwardedValues[0]
			if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, ",") || strings.Contains(value, "%") {
				respond.Error(writer, http.StatusBadRequest, "invalid_client_ip", "trusted client IP header is invalid for this connection", requestIDFromContext(request.Context()))
				return
			}
			parsed, err := netip.ParseAddr(value)
			if err != nil || parsed.Zone() != "" {
				respond.Error(writer, http.StatusBadRequest, "invalid_client_ip", "trusted client IP header is invalid for this connection", requestIDFromContext(request.Context()))
				return
			}
			client = parsed.Unmap()
		}
		if logState != nil {
			logState.clientIP = client.String()
		}
		ctx := context.WithValue(request.Context(), peerIPContextKey{}, peer)
		ctx = context.WithValue(ctx, clientIPContextKey{}, client)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func managementCIDRMiddleware(allowlist access.CIDRSet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if isManagementPath(request.URL.Path) {
			client, ok := clientIPFromContext(request.Context())
			if !ok || !allowlist.Contains(client) {
				respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func internalPeerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/internal" || strings.HasPrefix(request.URL.Path, "/internal/") {
			peer, ok := peerIPFromContext(request.Context())
			if !ok || !peer.IsLoopback() {
				respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func isManagementPath(path string) bool {
	for _, prefix := range []string{"/api/v1/auth", "/api/v1/panel", "/api/v1/admin"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func clientIPFromContext(ctx context.Context) (netip.Addr, bool) {
	address, ok := ctx.Value(clientIPContextKey{}).(netip.Addr)
	return address, ok && address.IsValid()
}

func peerIPFromContext(ctx context.Context) (netip.Addr, bool) {
	address, ok := ctx.Value(peerIPContextKey{}).(netip.Addr)
	return address, ok && address.IsValid()
}

func parseRemoteIP(remoteAddress string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	address, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil || address.Zone() != "" {
		return netip.Addr{}
	}
	return address.Unmap()
}
