package main

import (
	"strings"
	"testing"
)

func TestIngressTLSCommandRequiresAnExactModeShape(t *testing.T) {
	for _, arguments := range [][]string{
		{"config", "validate-ingress-tls"},
		{"config", "validate-ingress-tls", "domain", "panel.example.net"},
		{"config", "validate-ingress-tls", "ip"},
		{"config", "validate-ingress-tls", "admin-domain"},
		{"config", "validate-ingress-tls", "admin-domain", "admin.example.net", "extra"},
		{"config", "validate-ingress-tls", "admin-ip"},
		{"config", "validate-ingress-tls", "other", "value"},
	} {
		err := runConfigCommand(arguments)
		if err == nil || !strings.Contains(err.Error(), "validate-ingress-tls") {
			t.Fatalf("runConfigCommand(%q) error = %v", arguments, err)
		}
	}
}
