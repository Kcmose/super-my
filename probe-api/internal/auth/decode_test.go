package auth

import "testing"

func TestDecodeLoginRequestStrict(t *testing.T) {
	valid, err := DecodeLoginRequest([]byte(`{"username":"admin","password":"secret"}`))
	if err != nil || valid.Username != "admin" || valid.Password != "secret" {
		t.Fatalf("DecodeLoginRequest(valid) = %#v, %v", valid, err)
	}
	for name, body := range map[string]string{
		"missing":    `{"username":"admin"}`,
		"unknown":    `{"username":"admin","password":"secret","extra":true}`,
		"duplicate":  `{"username":"admin","password":"secret","password":"other"}`,
		"trailing":   `{"username":"admin","password":"secret"} {}`,
		"array":      `[]`,
		"whitespace": `{"username":" admin","password":"secret"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeLoginRequest([]byte(body)); err == nil {
				t.Fatalf("DecodeLoginRequest(%s) accepted invalid body", name)
			}
		})
	}
	invalidUTF8 := append([]byte(`{"username":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","password":"secret"}`)...)
	if _, err := DecodeLoginRequest(invalidUTF8); err == nil {
		t.Fatal("DecodeLoginRequest() accepted invalid UTF-8")
	}
}
