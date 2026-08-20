package dashboard

import "testing"

// TestAppImageBeyondBase covers the guard that decides whether a project's inner
// docker holds a real app image worth auto-saving as a baseline (vs. only the
// pulled service images, which aren't).
func TestAppImageBeyondBase(t *testing.T) {
	cases := []struct {
		name   string
		images []dindImage
		want   string // "" = nothing worth saving
	}{
		{"empty", nil, ""},
		{
			"only base services (the exact case that shouldn't auto-save)",
			[]dindImage{
				{Repository: "postgres", Tag: "16"},
				{Repository: "redis", Tag: "latest"},
				{Repository: "influxdb", Tag: "1.4.3"},
			},
			"",
		},
		{
			"app image present among services",
			[]dindImage{
				{Repository: "postgres", Tag: "16"},
				{Repository: "apm-app", Tag: "latest"},
			},
			"apm-app:latest",
		},
		{
			"registry-prefixed base service is still base",
			[]dindImage{{Repository: "library/redis", Tag: "7"}},
			"",
		},
		{
			"dangling <none> images are ignored",
			[]dindImage{{Repository: "<none>", Tag: "<none>"}},
			"",
		},
		{
			"app image with no tag",
			[]dindImage{{Repository: "myapp", Tag: "<none>"}},
			"myapp",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := appImageBeyondBase(c.images); got != c.want {
				t.Fatalf("appImageBeyondBase() = %q, want %q", got, c.want)
			}
		})
	}
}
