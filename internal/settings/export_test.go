package settings

// FailJournalWrites makes journal writes fail until the returned function
// runs.
func FailJournalWrites(err error) func() {
	old := writeJournalFile
	writeJournalFile = func(string, string) error { return err }
	return func() { writeJournalFile = old }
}
