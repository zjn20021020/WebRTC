package home

const MaxPlanSteps = 6

// Text is the self-contained request for this step, especially for general QA.
type Step struct {
	Action Action `json:"action"`
	Text   string `json:"text"`
}

type Plan struct {
	ID    string `json:"id"`
	Steps []Step `json:"steps"`
}
