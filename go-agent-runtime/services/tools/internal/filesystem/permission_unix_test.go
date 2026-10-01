//go:build unix

package filesystem

import "os"

// permissionBitsDenyAccess reports whether chmod-based permission denial is
// enforced for this test process: unix enforces mode bits for every account
// except the superuser.
func permissionBitsDenyAccess() bool { return os.Geteuid() != 0 }
