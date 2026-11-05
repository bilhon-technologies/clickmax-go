package clickmax

import (
	"bytes"
	"encoding/json"
)

// Nullable is a field that can be left out, set to null, or set to a value.
// The API treats the first two differently: an omitted field keeps the stored
// value, while null clears it. Request types use Nullable only where the API
// accepts null.
//
// The zero value is unset. Use [Null] to send null and [NewNullable] to send a
// value:
//
//	var phone clickmax.Nullable[string]      // omitted
//	phone = clickmax.Null[string]()          // sends null
//	phone = clickmax.NewNullable("+5511...") // sends "+5511..."
//
// In your own structs, give a Nullable field the omitzero JSON option, or an
// unset field is sent as null. Always use a named field: an embedded Nullable
// promotes its MarshalJSON, so the whole parent struct encodes as the value.
type Nullable[T any] struct {
	value T
	state nullableState
}

type nullableState uint8

const (
	nullableUnset nullableState = iota
	nullableNull
	nullableValue
)

// NewNullable returns a Nullable that sends v.
func NewNullable[T any](v T) Nullable[T] {
	return Nullable[T]{value: v, state: nullableValue}
}

// Null returns a Nullable that sends null.
func Null[T any]() Nullable[T] {
	return Nullable[T]{state: nullableNull}
}

// Get returns the value and true when n holds a value. It returns the zero
// value of T and false when n is unset or null.
func (n Nullable[T]) Get() (T, bool) {
	return n.value, n.state == nullableValue
}

// IsNull reports whether n is set to null.
func (n Nullable[T]) IsNull() bool {
	return n.state == nullableNull
}

// IsSet reports whether n is set, to null or to a value. Unset fields are left
// out of the JSON body.
func (n Nullable[T]) IsSet() bool {
	return n.state != nullableUnset
}

// IsZero reports whether n is unset. encoding/json calls it for fields tagged
// omitzero, so only unset fields are left out; null is still sent.
func (n Nullable[T]) IsZero() bool {
	return n.state == nullableUnset
}

// MarshalJSON writes the value, or null when n is null or unset.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.state != nullableValue {
		return []byte("null"), nil
	}
	return json.Marshal(n.value)
}

// UnmarshalJSON sets n to null for a JSON null and to the decoded value
// otherwise. A field absent from the JSON leaves n unset.
func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		var zero T
		*n = Nullable[T]{value: zero, state: nullableNull}
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*n = NewNullable(v)
	return nil
}

// Ptr returns a pointer to v. Optional fields and parameters are pointers, and
// Go cannot take the address of a literal such as 50 or "VIP":
//
//	perPage := clickmax.Ptr[int64](50)
func Ptr[T any](v T) *T {
	return &v
}
