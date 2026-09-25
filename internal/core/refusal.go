package core

// FieldError is a refusal of one field of a request: the value the operator
// typed there cannot work, and the device says which one it was.
//
// Field is the name of the field in the request body, exactly as it is
// spelled in JSON ("address", "netmask", "dns", "first", "mac"), so a screen
// can put the refusal next to the input without reading the sentence. Until
// #28 the screens matched the wording instead, and renaming "resolver" to
// "DNS server" nearly moved an error off its field without anything failing.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

// Refuse marks err as a refusal of field. A nil err stays nil, so it can wrap
// a validator's result directly.
func Refuse(field string, err error) error {
	if err == nil {
		return nil
	}
	return &FieldError{Field: field, Err: err}
}
