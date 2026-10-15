package semver

import "testing"

func TestLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.10.0", "0.9.0", false},
		{"1.2.0-beta.1", "1.2.0", true},
		{"1.2.0", "1.2.0-beta.1", false},
		{"1.2.0-beta.2", "1.2.0-beta.10", true},
		{"1.2.0-alpha", "1.2.0-beta", true},
		{"1.2.0-1", "1.2.0-alpha", true},
		{"1.2.0-beta", "1.2.0-beta.1", true},
		{"1.2.0+build.5", "1.2.0", false},
		{" 1.2.3 ", "1.2.3", false},
	}
	for _, c := range cases {
		got, err := Less(c.a, c.b)
		if err != nil {
			t.Fatalf("Less(%q, %q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("Less(%q, %q) = %v，应为 %v", c.a, c.b, got, c.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for _, s := range []string{"", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-01", "1.2.3+", "v1.2.3", "1.2.x", "1.2.3-a_b"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) 应该失败", s)
		}
	}
}
