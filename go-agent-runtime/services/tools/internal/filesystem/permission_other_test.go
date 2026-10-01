//go:build !unix

package filesystem

// permissionBitsDenyAccess reports whether chmod-based permission denial is
// enforced for this test process. Windows chmod does not deny reads or
// directory writes without ACL changes.
func permissionBitsDenyAccess() bool { return false }
