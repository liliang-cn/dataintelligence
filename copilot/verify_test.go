package copilot

import (
	"reflect"
	"testing"
)

func TestUnknownCodes(t *testing.T) {
	ev := `### query_metric {} {"rows":[["一厂 HP-501",12],["一厂 RR-302",3]]}`
	got := unknownCodes("HP-501 与 TR-302 故障最多，RR-302 次之，TR-302 要修", ev)
	if want := []string{"TR-302"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
