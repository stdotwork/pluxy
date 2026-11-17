package protocol

// LongestForm returns the length in bytes of the longest form r removes (the credential's
// spellings that String looks for as they are, such as JSON-escaped or base64), or 0 when r
// removes nothing. Text that cuts a body near its middle uses it to bound how far from the cut a
// part of a credential can reach.
func (r *Redactor) LongestForm() int {
	if r == nil || len(r.forms) == 0 {
		return 0
	}
	return len(r.forms[0])
}
