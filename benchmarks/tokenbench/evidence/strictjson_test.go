package evidence

import "testing"

func TestDecodeStrictRequiresCanonicalRequiredFields(t *testing.T) {
	t.Parallel()
	type document struct {
		Values  []string `json:"values"`
		Enabled bool     `json:"enabled"`
		Count   int      `json:"count"`
	}
	for _, raw := range []string{
		`{}`,
		`{"values":[],"enabled":false}`,
		`{"values":[],"enabled":false,"count":0 }`,
		`{"enabled":false,"values":[],"count":0}`,
	} {
		var decoded document
		if err := decodeStrict([]byte(raw), &decoded); err == nil {
			t.Fatalf("noncanonical or incomplete JSON was accepted: %s", raw)
		}
	}
	var decoded document
	if err := decodeStrict(
		[]byte(`{"values":[],"enabled":false,"count":0}`),
		&decoded,
	); err != nil {
		t.Fatalf("canonical explicit fields were rejected: %v", err)
	}
}
