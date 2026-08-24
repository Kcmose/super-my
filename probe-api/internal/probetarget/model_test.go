package probetarget

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const validNodeID = "11111111-1111-4111-8111-111111111111"

func TestDecodeCreateStrictAndTypeRules(t *testing.T) {
	valid := `{"node_id":"11111111-1111-4111-8111-111111111111","name":"TLS","type":"https","host":"example.com","port":443,"path":null,"interval_seconds":10,"timeout_seconds":10,"retention_seconds":7776000,"enabled":true}`
	request, err := DecodeCreate([]byte(valid))
	if err != nil {
		t.Fatalf("DecodeCreate(valid) error = %v", err)
	}
	values, err := request.normalized()
	if err != nil || values.Path == nil || *values.Path != "/" || values.RetentionSeconds != MaxRetentionSeconds {
		t.Fatalf("normalized valid request = %#v, %v", values, err)
	}

	for name, body := range map[string]string{
		"icmp disabled":  strings.Replace(valid, `"type":"https"`, `"type":"icmp"`, 1),
		"unknown":        strings.TrimSuffix(valid, "}") + `,"command":"whoami"}`,
		"duplicate":      strings.Replace(valid, `"name":"TLS"`, `"name":"TLS","name":"other"`, 1),
		"missing":        strings.Replace(valid, `,"enabled":true`, "", 1),
		"trailing":       valid + `{}`,
		"scheme host":    strings.Replace(valid, `"host":"example.com"`, `"host":"https://example.com"`, 1),
		"query path":     strings.Replace(valid, `"path":null`, `"path":"/health?secret=1"`, 1),
		"fragment path":  strings.Replace(valid, `"path":null`, `"path":"/health#part"`, 1),
		"timeout":        strings.Replace(valid, `"timeout_seconds":10`, `"timeout_seconds":11`, 1),
		"over retention": strings.Replace(valid, `"retention_seconds":7776000`, `"retention_seconds":7776001`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCreate([]byte(body)); err == nil {
				t.Fatal("DecodeCreate() accepted invalid input")
			}
		})
	}
}

func TestTCPAndHTTPConditionalFields(t *testing.T) {
	port := int32(1)
	tcp := CreateRequest{
		NodeID: validNodeID, Name: "TCP", Type: TypeTCP, Host: "192.0.2.10",
		Port: &port, IntervalSeconds: 10, TimeoutSeconds: 1, RetentionSeconds: 1, Enabled: true,
	}
	if _, err := tcp.normalized(); err != nil {
		t.Fatalf("TCP lower boundary error = %v", err)
	}
	tcp.Port = nil
	if _, err := tcp.normalized(); err == nil {
		t.Fatal("TCP without port was accepted")
	}
	path := "/health"
	tcp.Port = &port
	tcp.Path = &path
	if _, err := tcp.normalized(); err == nil {
		t.Fatal("TCP with path was accepted")
	}

	https := tcp
	https.Type = TypeHTTPS
	https.Port = nil
	https.Path = nil
	if values, err := https.normalized(); err != nil || values.Path == nil || *values.Path != "/" {
		t.Fatalf("HTTPS defaults = %#v, %v", values, err)
	}
}

func TestUpdateMergeRevalidatesCompleteTarget(t *testing.T) {
	port := int32(443)
	path := "/"
	current := Target{
		TargetID: "22222222-2222-4222-8222-222222222222", NodeID: validNodeID,
		Name: "HTTPS", Type: TypeHTTPS, Host: "example.com", Port: &port, Path: &path,
		IntervalSeconds: 30, TimeoutSeconds: 3, RetentionSeconds: 86400, Enabled: true,
		ConfigVersion: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	tcp := TypeTCP
	if _, err := mergeUpdate(current, UpdateRequest{Type: &tcp}); err == nil {
		t.Fatal("type transition retained an HTTP path")
	}
	nullPath := NullableString{Set: true, Value: nil}
	if values, err := mergeUpdate(current, UpdateRequest{Type: &tcp, Path: nullPath}); err != nil || values.Type != TypeTCP || values.Path != nil {
		t.Fatalf("valid type transition = %#v, %v", values, err)
	}
	icmp := Type("icmp")
	if _, err := mergeUpdate(current, UpdateRequest{Type: &icmp}); err == nil {
		t.Fatal("ICMP update was accepted")
	}
}

func TestRetentionErrorHasStableCode(t *testing.T) {
	port := int32(443)
	request := CreateRequest{
		NodeID: validNodeID, Name: "TLS", Type: TypeHTTPS, Host: "example.com", Port: &port,
		IntervalSeconds: 30, TimeoutSeconds: 3, RetentionSeconds: MaxRetentionSeconds + 1, Enabled: true,
	}
	_, err := request.normalized()
	var fieldError *FieldError
	if !errors.As(err, &fieldError) || fieldError.Code != "retention_exceeds_limit" {
		t.Fatalf("normalized() error = %v", err)
	}
}

func TestCursorRoundTripAndValidation(t *testing.T) {
	want := Cursor{CreatedAt: time.Date(2026, 8, 22, 1, 2, 3, 4, time.UTC), TargetID: "22222222-2222-4222-8222-222222222222"}
	encoded, err := EncodeCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCursor(encoded)
	if err != nil || got.TargetID != want.TargetID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("DecodeCursor() = %#v, %v", got, err)
	}
	duplicate := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"v":1,"created_at":"2026-08-22T01:02:03Z","target_id":"22222222-2222-4222-8222-222222222222"}`))
	for _, invalid := range []string{"", "not-base64", encoded + "=", duplicate} {
		if _, err := DecodeCursor(invalid); err == nil {
			t.Fatalf("DecodeCursor(%q) accepted invalid cursor", invalid)
		}
	}
}
