package auth

import "testing"

func TestOnlyAdministratorIsAValidAccountRole(t *testing.T) {
	if !validRole(RoleAdmin) {
		t.Fatal("administrator role was rejected")
	}
	if validRole(RoleViewer) {
		t.Fatal("legacy viewer role was accepted as an account role")
	}
	if validRole(Role("guest")) {
		t.Fatal("anonymous guest label was accepted as an account role")
	}
}
