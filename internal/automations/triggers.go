package automations

// Triggers are the user-facing framing of the engine's events. The UI shows
// each trigger as a card ("When you APPROVE a PR") whose first step is the
// hard-coded built-in behavior ("Approve on GitHub"), followed by any user-added
// steps. This hides the "event"/"hook" vocabulary: a user just extends what
// already happens. The Event field maps a trigger back to the wire event a hook
// binds to, so the redesigned UI can create hooks without exposing the term.

// Trigger describes one user-facing automation point.
type Trigger struct {
	Event       string `json:"event"`       // the underlying event (hook binds to this)
	Title       string `json:"title"`       // "When you approve a PR"
	Verb        string `json:"verb"`        // short label, e.g. "Approve"
	Description string `json:"description"`  // one line explaining when it fires
	Builtin     string `json:"builtin"`     // the built-in step shown as step 1 ("" = none)
	BuiltinIcon string `json:"builtinIcon"` // a glyph for the built-in step
}

// Triggers returns the catalog in display order. This is the single source of
// truth for the redesigned Automations UI.
func Triggers() []Trigger {
	return []Trigger{
		{
			Event: EventPRApprove, Title: "When you approve a PR", Verb: "Approve",
			Description: "Runs when you approve a pull request from the review page.",
			Builtin:     "Approve on GitHub", BuiltinIcon: "✓",
		},
		{
			Event: EventPRRequestChanges, Title: "When you request changes", Verb: "Request changes",
			Description: "Runs when you request changes on a pull request.",
			Builtin:     "Request changes on GitHub", BuiltinIcon: "✗",
		},
		{
			Event: EventPRComment, Title: "When you comment on a PR", Verb: "Comment",
			Description: "Runs when you post a comment on a pull request.",
			Builtin:     "Comment on GitHub", BuiltinIcon: "💬",
		},
		{
			Event: EventPRMerge, Title: "When you merge a PR", Verb: "Merge",
			Description: "Runs when you merge a pull request.",
			Builtin:     "Merge on GitHub", BuiltinIcon: "⑃",
		},
		{
			Event: EventPRAnalyze, Title: "When AI analysis runs", Verb: "Analyze",
			Description: "Runs after Claude analyzes a pull request's changes.",
			Builtin:     "Analyze the PR with Claude", BuiltinIcon: "✨",
		},
		{
			Event: EventPREnter, Title: "When you open a PR", Verb: "Open PR",
			Description: "Runs when you open a pull request's review page.",
			Builtin:     "", BuiltinIcon: "",
		},
		{
			Event: EventProjectStart, Title: "When a project starts", Verb: "Project start",
			Description: "Runs when a sandbox project launches.",
			Builtin:     "Launch the sandbox", BuiltinIcon: "▶",
		},
	}
}

// TriggerFor returns the Trigger for an event, or false if unknown.
func TriggerFor(event string) (Trigger, bool) {
	for _, t := range Triggers() {
		if t.Event == event {
			return t, true
		}
	}
	return Trigger{}, false
}
