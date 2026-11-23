package app

import "time"

// A provider lease can start before Devflow finishes preparing the preview.
// Multi-write operations must still be applicable on every bound connection.
func earliestApprovalExpiry(bounds ...time.Time) time.Time {
	var earliest time.Time
	for _, deadline := range bounds {
		if !deadline.IsZero() && (earliest.IsZero() || deadline.Before(earliest)) {
			earliest = deadline
		}
	}
	return earliest
}

func (w *jiraWrite) expiresAt() time.Time {
	if w == nil {
		return time.Time{}
	}
	return w.Binding.ExpiresAt
}

func (w *boundTaskWrite) expiresAt() time.Time {
	if w == nil {
		return time.Time{}
	}
	return w.Binding.ExpiresAt
}

func (s *jiraStartWrites) expiresAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	deadline := s.Assignment.expiresAt()
	if s.Transition != nil {
		deadline = earliestApprovalExpiry(deadline, s.Transition.Write.expiresAt())
	}
	return deadline
}

func slackApprovalExpiry(plans []nativeSlackPlan) time.Time {
	var deadline time.Time
	for _, plan := range plans {
		deadline = earliestApprovalExpiry(deadline, plan.Binding.ExpiresAt)
	}
	return deadline
}
