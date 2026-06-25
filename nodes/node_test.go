package nodes

import "testing"

func TestMergeFieldRules(t *testing.T) {
	n := &Node{
		Priority: []string{"crm", "import"}, // crm wins source-priority conflicts
		Rules: []Rule{
			{Field: "email", Kind: KindConflict, Strategy: "latest"},
			{Field: "segment", Kind: KindConflict, Strategy: "source_priority"},
			{Field: "ltv", Kind: KindConflict, Strategy: "max"},
			{Field: "name", Kind: KindRequire},
			{Field: "ltv", Kind: KindAlert, Strategy: "range", Params: map[string]any{"max": 100000.0}},
			{Field: "email", Kind: KindAlert, Strategy: "changed"},
		},
	}
	existing := Source{Name: "crm", Time: "2024-01-01T00:00:00Z", Rec: map[string]string{
		"name": "Ada", "email": "ada@old.com", "segment": "smb", "ltv": "5000",
	}}
	incoming := Source{Name: "import", Time: "2025-01-01T00:00:00Z", Rec: map[string]string{
		"name": "Ada", "email": "ada@new.com", "segment": "enterprise", "ltv": "250000",
	}}

	got, events := n.Merge(existing, incoming)

	// email: latest → incoming (newer time)
	if got["email"] != "ada@new.com" {
		t.Errorf("email = %q, want ada@new.com (latest)", got["email"])
	}
	// segment: source_priority → crm wins → keep existing "smb"
	if got["segment"] != "smb" {
		t.Errorf("segment = %q, want smb (crm priority)", got["segment"])
	}
	// alerts: email changed + ltv above max
	var changed, overMax bool
	for _, e := range events {
		if e.Field == "email" && e.Rule == KindAlert {
			changed = true
		}
		if e.Field == "ltv" && e.Rule == KindAlert {
			overMax = true
		}
	}
	if !changed {
		t.Error("expected an email 'changed' alert")
	}
	if !overMax {
		t.Error("expected an ltv 'range' alert (above max)")
	}
}

func TestRequireFlagsEmpty(t *testing.T) {
	n := &Node{Rules: []Rule{{Field: "email", Kind: KindRequire}}}
	_, events := n.Merge(
		Source{Name: "a", Rec: map[string]string{"email": ""}},
		Source{Name: "b", Rec: map[string]string{"email": ""}},
	)
	found := false
	for _, e := range events {
		if e.Field == "email" && e.Rule == KindRequire && e.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Error("expected a require error for empty email")
	}
}
