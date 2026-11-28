package protocol

// LongestForm returns the length in bytes of the longest form r removes for which keep returns
// true, or of the longest of all of them when keep is nil; 0 when there is none.
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
