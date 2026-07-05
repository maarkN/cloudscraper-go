package main

import "testing"

func TestHeaderOptionsParsesValid(t *testing.T) {
	opts, err := headerOptions([]string{"Authorization: Bearer abc", "X-Api-Key:  k1  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("got %d options, want 2", len(opts))
	}
}

func TestHeaderOptionsRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"no-colon-here", ": empty name", "   : x"} {
		if _, err := headerOptions([]string{bad}); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}
