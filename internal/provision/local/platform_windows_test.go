package local

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsManifestACL(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid
	for _, tc := range []struct {
		acl  string
		safe bool
	}{
		{"(A;;FA;;;" + sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)", true},
		{"(A;;FR;;;BU)", true},
		{"(A;;FW;;;BU)", false},
		{"(A;;WD;;;WD)", false},
		{"(A;;SD;;;AU)", false},
		{"(A;;DC;;;BU)", false},
	} {
		sd, err := windows.SecurityDescriptorFromString("D:" + tc.acl)
		if err != nil {
			t.Fatal(err)
		}
		if got := restrictedACL(sd, sid); got != tc.safe {
			t.Errorf("DACL %s trusted = %t", tc.acl, got)
		}
	}
}
