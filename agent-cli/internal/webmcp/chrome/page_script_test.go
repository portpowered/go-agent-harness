package chrome

import (
	"context"
	"errors"
)

func (s *targetSession) installPageScript(ctx context.Context, source string) error {
	if source == "" {
		return errors.New("site adapter source is empty")
	}
	return s.run(ctx, pageScriptAction(source))
}
