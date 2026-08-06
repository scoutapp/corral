package config

import (
	"reflect"
	"testing"
)

func TestMitmPresetOrDefault(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProjectConfig
		want string
	}{
		{"unset defaults to minimal", ProjectConfig{}, "minimal"},
		{"explicit preset wins", ProjectConfig{MitmPreset: "all"}, "all"},
		{"legacy host list => custom", ProjectConfig{MonitorHosts: []string{"api.github.com"}}, "custom"},
		{"preset overrides legacy hosts", ProjectConfig{MitmPreset: "none", MonitorHosts: []string{"x"}}, "none"},
	}
	for _, c := range cases {
		if got := c.cfg.MitmPresetOrDefault(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestResolveMonitorHosts(t *testing.T) {
	// all -> empty (proxy treats empty as monitor-all)
	if got := (&ProjectConfig{MitmPreset: "all"}).ResolveMonitorHosts(); len(got) != 0 {
		t.Errorf("all: got %v, want empty", got)
	}
	// none -> the never-matching sentinel only
	none := (&ProjectConfig{MitmPreset: "none"}).ResolveMonitorHosts()
	if !reflect.DeepEqual(none, []string{MonitorNoneSentinel}) {
		t.Errorf("none: got %v, want [%s]", none, MonitorNoneSentinel)
	}
	// minimal -> the curated claude+github set (and includes both)
	min := (&ProjectConfig{MitmPreset: "minimal"}).ResolveMonitorHosts()
	has := map[string]bool{}
	for _, h := range min {
		has[h] = true
	}
	if !has["api.anthropic.com"] || !has["api.github.com"] {
		t.Errorf("minimal missing expected hosts: %v", min)
	}
	// custom -> the explicit list with the mandatory hosts always unioned in
	// (they can't be removed — credential injection depends on them).
	custom := (&ProjectConfig{MitmPreset: "custom", MonitorHosts: []string{"only.example.com"}}).ResolveMonitorHosts()
	chas := map[string]bool{}
	for _, h := range custom {
		chas[h] = true
	}
	if !chas["only.example.com"] {
		t.Errorf("custom missing the user's host: %v", custom)
	}
	for _, m := range MandatoryMonitorHosts {
		if !chas[m] {
			t.Errorf("custom dropped mandatory host %s: %v", m, custom)
		}
	}
	// A custom list that OMITS the mandatory hosts must still include them.
	omit := (&ProjectConfig{MitmPreset: "custom", MonitorHosts: []string{"only.example.com"}}).ResolveMonitorHosts()
	found := false
	for _, h := range omit {
		if h == "api.anthropic.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom must force api.anthropic.com even when omitted: %v", omit)
	}
	// default (unset) resolves to minimal
	def := (&ProjectConfig{}).ResolveMonitorHosts()
	if len(def) == 0 {
		t.Error("default should resolve to the minimal set, got empty")
	}
}
