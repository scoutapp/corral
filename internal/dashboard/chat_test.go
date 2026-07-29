package dashboard

import (
	"reflect"
	"testing"
)

func TestParseChatTools(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty falls back to read-only default", "", chatDefaultTools},
		{"whitespace falls back to default", "   ", chatDefaultTools},
		{"single valid tool", "Read", []string{"Read"}},
		{"grant bash explicitly", "Read,Bash", []string{"Read", "Bash"}},
		{"unknown tools dropped", "Read,Nuke,Grep", []string{"Read", "Grep"}},
		{"only unknowns falls back to default", "Nuke,Rm", chatDefaultTools},
		{"dedupes and trims", " Read , Read , Glob ", []string{"Read", "Glob"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseChatTools(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseChatTools(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
