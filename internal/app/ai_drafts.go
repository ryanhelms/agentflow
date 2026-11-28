package app

// DraftSecrets runs the commit flow's diff scanner over the filtered diff
// text a draft would send (ADR "Phase 8 prompt context", Filter step 5):
// the likely secrets in its added lines. A hit refuses the draft
// secret_in_context; the hit's Line is cut and is never shown by a draft.
func DraftSecrets(diff string) []SecretHit { return scanDiff(diff) }
