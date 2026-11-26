package settings

// FailJournalWrites makes journal writes fail until the returned function
// runs.
func FailJournalWrites(err error) func() {
	old := writeJournalFile
	writeJournalFile = func(string, string) error { return err }
	return func() { writeJournalFile = old }
}

// FailPersonalWrites makes personal layer writes fail until the returned
// function runs.
func FailPersonalWrites(err error) func() {
	old := writePersonalFile
	writePersonalFile = func(string, string) error { return err }
	return func() { writePersonalFile = old }
}
