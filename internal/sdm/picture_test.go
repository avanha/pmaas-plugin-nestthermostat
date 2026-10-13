package sdm

import "testing"

func TestSafePictureURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://lh3.googleusercontent.com/a/abc=s96-c", "https://lh3.googleusercontent.com/a/abc=s96-c"},
		{"", ""},
		{"http://example.com/p.png", ""},
		{"javascript:alert(1)", ""},
		{"data:image/png;base64,AAAA", ""},
		{"//example.com/p.png", ""},
		{"https:///nohost", ""},
		{"https://bad host/%zz", ""},
	} {
		if got := safePictureURL(c.in); got != c.want {
			t.Errorf("safePictureURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
