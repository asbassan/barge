package build

import "testing"

func TestSubstituteArgs(t *testing.T) {
	args := map[string]string{"VERSION": "3.11", "NAME": "tessa"}
	cases := []struct{ in, want string }{
		{"python${VERSION}", "python3.11"},
		{"$NAME app", "tessa app"},
		{"no-vars", "no-vars"},
	}
	for _, tc := range cases {
		if got := substituteArgs(tc.in, args); got != tc.want {
			t.Errorf("substituteArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestToWindowsPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/app", `C:\app`},
		{"/app/sub/dir", `C:\app\sub\dir`},
		{`C:/app`, `C:\app`},
		{`C:\app`, `C:\app`},
		{`D:\data`, `D:\data`},
		{"relative", "relative"},
	}
	for _, tc := range cases {
		got := toWindowsPath(tc.in)
		if got != tc.want {
			t.Errorf("toWindowsPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
