package bot

import "testing"

func TestSupportURL(t *testing.T) {
	cases := map[string]string{
		"@alighaemi9731":             "https://t.me/alighaemi9731",
		"alighaemi9731":              "https://t.me/alighaemi9731",
		"https://t.me/alighaemi9731": "https://t.me/alighaemi9731",
		"t.me/alighaemi9731":         "https://t.me/alighaemi9731",
		"https://example.com/x":      "https://example.com/x",
		"":                           "",
		"call me 0912":               "", // contains spaces -> not a clean handle
	}
	for in, want := range cases {
		if got := supportURL(in); got != want {
			t.Errorf("supportURL(%q) = %q, want %q", in, got, want)
		}
	}
}
