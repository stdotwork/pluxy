package protocol

// LongestForm returns the length in bytes of the longest form r removes (the credential's
// spellings that String looks for as they are, such as JSON-escaped or base64) for which keep
// returns true, or of the longest of all of them when keep is nil; 0 when there is none.
func (r *Redactor) LongestForm(keep func(form string) bool) int {
	if r == nil {
		return 0
	}
	for _, f := range r.forms {
		if keep == nil || keep(f) {
			return len(f)
		}
	}
	return 0
}
