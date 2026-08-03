package connectors

import "testing"

// SAP zero-pads document numbers, item positions, material numbers and cost
// centres. Landing "000010" as an integer turns it into 10, and the column can
// no longer be joined back to the system it came from.
func TestZeroPaddedCodesStayText(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{"posnr", []string{"000010", "000020"}, "text"},
		{"one padded value is enough", []string{"10", "20", "000030"}, "text"},
		{"real integers", []string{"10", "20", "0"}, "int"},
		{"a decimal starting in zero is a number", []string{"0.5", "1.25"}, "numeric"},
	}
	for _, c := range cases {
		if got := inferType(c.values); got != c.want {
			t.Errorf("%s: inferType(%v) = %q, want %q", c.name, c.values, got, c.want)
		}
	}
}
