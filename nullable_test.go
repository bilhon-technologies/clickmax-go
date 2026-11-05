package clickmax

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNullableJSONRoundTrip(t *testing.T) {
	type input struct {
		Unset     Nullable[string] `json:"unset,omitzero"`
		Null      Nullable[string] `json:"null,omitzero"`
		ZeroInt   Nullable[int64]  `json:"zeroInt,omitzero"`
		EmptyText Nullable[string] `json:"emptyText,omitzero"`
	}
	in := input{
		Null:      Null[string](),
		ZeroInt:   NewNullable[int64](0),
		EmptyText: NewNullable(""),
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"null":null,"zeroInt":0,"emptyText":""}`; string(data) != want {
		t.Errorf("Marshal = %s, want %s", data, want)
	}

	var out input
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}
