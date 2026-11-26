package auth

import "testing"

func TestRoleValid(t *testing.T) {
	tests := []struct {
		role Role
		want bool
	}{
		{role: RoleOwner, want: true},
		{role: RoleAdmin, want: true},
		{role: RoleStaff, want: true},
		{role: RoleClient, want: true},
		{role: Role(""), want: false},
		{role: Role("superadmin"), want: false},
		{role: Role("Owner"), want: false},
	}

	for _, tc := range tests {
		if got := tc.role.Valid(); got != tc.want {
			t.Errorf("Role(%q).Valid() = %v, se esperaba %v", string(tc.role), got, tc.want)
		}
	}
}

// TestRoleCapabilities pins the basis for the role-filtered tools/list of RF2:
// owner/admin configuration capabilities are denied to staff and clients, and
// any unrecognized role is denied everything (fail-closed).
func TestRoleCapabilities(t *testing.T) {
	tests := []struct {
		role         Role
		wantInternal bool
		wantManage   bool
	}{
		{role: RoleOwner, wantInternal: true, wantManage: true},
		{role: RoleAdmin, wantInternal: true, wantManage: true},
		{role: RoleStaff, wantInternal: true, wantManage: false},
		{role: RoleClient, wantInternal: false, wantManage: false},
		{role: Role(""), wantInternal: false, wantManage: false},
		{role: Role("superadmin"), wantInternal: false, wantManage: false},
	}

	for _, tc := range tests {
		if got := tc.role.IsInternal(); got != tc.wantInternal {
			t.Errorf("Role(%q).IsInternal() = %v, se esperaba %v", string(tc.role), got, tc.wantInternal)
		}
		if got := tc.role.CanManageWorkflows(); got != tc.wantManage {
			t.Errorf("Role(%q).CanManageWorkflows() = %v, se esperaba %v", string(tc.role), got, tc.wantManage)
		}
	}
}

// TestStaffHasNoOwnerAdminCapabilities states the RF2 criterion directly:
// "Dado un caller `staff`, cuando pide `tools/list`, entonces no aparecen tools
// de administración del Owner."
func TestStaffHasNoOwnerAdminCapabilities(t *testing.T) {
	if RoleStaff.CanManageWorkflows() {
		t.Fatal("staff no debe obtener herramientas de administración del Owner/Admin")
	}
	if !RoleStaff.IsInternal() {
		t.Fatal("staff es una cuenta interna del negocio")
	}
	for _, role := range []Role{RoleOwner, RoleAdmin} {
		if !role.CanManageWorkflows() {
			t.Fatalf("%q debe poder configurar los workflows del negocio", string(role))
		}
	}
}
