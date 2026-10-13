package main

import "testing"

func TestParsePlanStep(t *testing.T) {
	cases := []struct {
		raw, owner, title string
		ok                bool
	}{
		{"孙悟空=Build the API", "孙悟空", "Build the API", true},
		{" Kun = Decide a=b split ", "Kun", "Decide a=b split", true},
		{"no separator", "", "", false},
		{"=title only", "", "", false},
		{"owner only=", "", "", false},
	}
	for _, c := range cases {
		owner, title, err := parsePlanStep(c.raw)
		if (err == nil) != c.ok || owner != c.owner || title != c.title {
			t.Errorf("parsePlanStep(%q) = %q, %q, %v", c.raw, owner, title, err)
		}
	}
}
