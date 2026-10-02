package probe

import (
	"fmt"
	"time"
)

func (c *PatienceController) ObserveToolProgress(duration time.Duration, detail string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	if duration < 0 {
		return fmt.Errorf("%w: tool progress duration must not be negative", ErrInvalidPatienceEvidence)
	}
	if !c.responseStarted {
		if err := c.ObserveResponseStart("response started with observable tool work"); err != nil {
			return err
		}
	}
	at, err := c.elapsed()
	if err != nil {
		return err
	}
	if at < c.lastProgress {
		return fmt.Errorf("%w: tool progress begins before previous progress interval ends", ErrPatienceClockRegression)
	}
	if err := c.appendEvent(PatienceEventToolProgress, at, duration, detail); err != nil {
		return err
	}
	c.recordProgress(addPatienceDuration(at, duration))
	c.activityState = PatienceActivityTool
	return nil
}
