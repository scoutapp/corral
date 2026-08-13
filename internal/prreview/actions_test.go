package prreview

import "testing"

// These exercise the validation logic that doesn't require a live `gh` (body
// requirements, merge-method mapping). The gh invocation itself isn't unit-
// tested (it writes to GitHub).

func TestRequestChangesRequiresBody(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)
	if err := svc.RequestChanges(prID, "o/n", ""); err == nil {
		t.Error("request-changes with empty body should error")
	}
}

func TestCommentRequiresBody(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)
	if err := svc.Comment(prID, "o/n", "   "); err == nil {
		t.Error("comment with blank body should error")
	}
}

func TestMergeInvalidMethod(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)
	if err := svc.Merge(prID, "o/n", "bogus"); err == nil {
		t.Error("invalid merge method should error")
	}
}

func TestLineCommentNeedsHeadSHA(t *testing.T) {
	svc, _ := newService(t)
	// seedPR stores no head_sha → line comment should refuse.
	prID := seedPR(t, svc, "r1", sampleDiff)
	if err := svc.LineComment(prID, "o/n", "hi", "a.go", 5, "RIGHT"); err == nil {
		t.Error("line comment without head sha should error")
	}
}

func TestRepoIDForPR(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "repo-xyz", sampleDiff)
	got, err := svc.RepoIDForPR(prID)
	if err != nil || got != "repo-xyz" {
		t.Errorf("RepoIDForPR = %q,%v want repo-xyz", got, err)
	}
}
