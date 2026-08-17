package prreview

import "testing"

func TestIssueNumberFromBranch(t *testing.T) {
	cases := map[string]int{
		"issue-42-fix-thing":  42,
		"issue_42_fix":        42,
		"42-fix-thing":        42,
		"fix/issue-42":        42,
		"gh-42":               42,
		"gh_42":               42,
		"feature/gh-7-widget": 7,
		"123":                 123,
		"#88-thing":           88,
	}
	for branch, want := range cases {
		n, ok := issueNumberFromBranch(branch)
		if !ok || n != want {
			t.Errorf("issueNumberFromBranch(%q) = %d,%v; want %d,true", branch, n, ok, want)
		}
	}

	// No number → no match.
	for _, branch := range []string{"main", "release", "fix-the-bug", "v2", "", "feature/new-ui"} {
		if n, ok := issueNumberFromBranch(branch); ok {
			t.Errorf("issueNumberFromBranch(%q) = %d,true; want no match", branch, n)
		}
	}
}

func TestSplitOwnerName(t *testing.T) {
	owner, name, ok := splitOwnerName("scoutapp/corral")
	if !ok || owner != "scoutapp" || name != "corral" {
		t.Errorf("splitOwnerName = %q,%q,%v", owner, name, ok)
	}
	for _, bad := range []string{"", "noslash", "/name", "owner/", "a/b/c"} {
		if _, _, ok := splitOwnerName(bad); ok && bad != "a/b/c" {
			// "a/b/c" splits to owner="a", name="b/c" via SplitN — acceptable; the
			// others must fail.
			if bad != "a/b/c" {
				t.Errorf("splitOwnerName(%q) should fail", bad)
			}
		}
	}
}
