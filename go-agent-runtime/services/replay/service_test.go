package replay

import (
	"errors"
	"testing"
)

func TestStrictPreparedValidateCompleteRequiresPreparation(t *testing.T) {
	if err := (StrictPrepared{}).ValidateComplete(); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("ValidateComplete() error = %v, want ErrBundleIncomplete", err)
	}
}
