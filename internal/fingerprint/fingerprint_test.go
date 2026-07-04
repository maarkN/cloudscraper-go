package fingerprint

import "testing"

func TestGetKnownProfiles(t *testing.T) {
	for _, name := range Names() {
		p, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) returned error: %v", name, err)
		}
		if p.Name != name {
			t.Errorf("profile %q has Name %q", name, p.Name)
		}
		if len(p.Headers) == 0 {
			t.Errorf("profile %q has no default headers", name)
		}
		var hasUA bool
		for _, h := range p.Headers {
			if h.Name == "User-Agent" && h.Value != "" {
				hasUA = true
			}
		}
		if !hasUA {
			t.Errorf("profile %q is missing a User-Agent header", name)
		}
	}
}

func TestGetDefault(t *testing.T) {
	p, err := Get("")
	if err != nil {
		t.Fatalf("Get(\"\") returned error: %v", err)
	}
	if p.Name != DefaultProfile {
		t.Errorf("empty name resolved to %q, want %q", p.Name, DefaultProfile)
	}
}

func TestGetUnknown(t *testing.T) {
	if _, err := Get("netscape"); err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
}
