package nleval

import "testing"

func TestSplitClassifiesCells(t *testing.T) {
	nums, labels := split([][]any{
		{"South", 1234.5},
		{"North", int64(10)},
		{"2024-01-01 00:00:00 +0000 UTC", nil},
	})
	if len(nums) != 2 {
		t.Fatalf("want 2 numeric cells, got %d (%v)", len(nums), nums)
	}
	if len(labels) != 3 { // "South", "North", and the date string (non-numeric)
		t.Fatalf("want 3 label cells (regions + date), got %d (%v)", len(labels), labels)
	}
}

func TestFloatsEqOrderInsensitiveWithinTolerance(t *testing.T) {
	a := []float64{3, 1, 2}
	b := []float64{2.0000001, 1, 3}
	if !floatsEq(a, b, 1e-6) {
		t.Fatal("expected equal within tolerance, order-insensitive")
	}
	if floatsEq([]float64{1, 2}, []float64{1, 2, 3}, 1e-6) {
		t.Fatal("different lengths must not be equal")
	}
	if floatsEq([]float64{100}, []float64{101}, 1e-6) {
		t.Fatal("1% apart must not be equal at 1e-6 tolerance")
	}
}

func TestStringsEqIsSetwise(t *testing.T) {
	if !stringsEq([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("set-equal label sets should match regardless of order")
	}
	if stringsEq([]string{"a"}, []string{"a", "a"}) {
		t.Fatal("multiplicity matters")
	}
}

func TestSetEqMetricSets(t *testing.T) {
	if !setEq([]string{"total_revenue"}, []string{"total_revenue"}) {
		t.Fatal("identical metric sets should be equal")
	}
	if setEq([]string{"total_revenue"}, []string{"net_revenue"}) {
		t.Fatal("different metrics must not be equal")
	}
	if setEq([]string{"a", "b"}, []string{"a"}) {
		t.Fatal("different sizes must not be equal")
	}
}

func TestReportFinalizeAndGate(t *testing.T) {
	r := &Report{Cases: []CaseResult{
		{Case: "a", Category: "simple", Passed: true, Expected: []string{"x"}, Predicted: []string{"x"}},
		{Case: "b", Category: "simple", Passed: false, Expected: []string{"x"}, Predicted: []string{"y"}},
		{Case: "c", Category: "governance", Passed: true, Expected: []string{"z"}, Predicted: []string{"z"}},
		{Case: "d", Category: "ambiguous", Skipped: true},
	}}
	r.finalize()
	if r.Total != 3 || r.Passed != 2 || r.Skipped != 1 {
		t.Fatalf("totals: total=%d passed=%d skipped=%d", r.Total, r.Passed, r.Skipped)
	}
	if r.Acc < 0.66 || r.Acc > 0.67 {
		t.Fatalf("accuracy = %.3f, want ~0.667", r.Acc)
	}
	// Confusion must record the x→y miss as incorrect.
	var sawMiss bool
	for _, e := range r.Confusion {
		if e.Expected == "x" && e.Predicted == "y" && !e.Correct {
			sawMiss = true
		}
	}
	if !sawMiss {
		t.Fatal("confusion matrix should record the x→y miss")
	}
	// Gate: overall floor + per-category floor.
	if ok, _ := r.Gate(0.5, nil); !ok {
		t.Fatal("should pass a 50% floor")
	}
	if ok, fails := r.Gate(0.9, nil); ok || len(fails) == 0 {
		t.Fatal("should fail a 90% floor with reasons")
	}
	if ok, _ := r.Gate(0.5, map[string]float64{"governance": 1.0}); !ok {
		t.Fatal("governance is 100%, floor 1.0 should pass")
	}
	if ok, _ := r.Gate(0.5, map[string]float64{"simple": 1.0}); ok {
		t.Fatal("simple is 50%, floor 1.0 should fail")
	}
}
