package audiooutput

// Route describes the system default output, not a browser-specific override.
type Route struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Source string `json:"source"`
	Reason string `json:"reason,omitempty"`
}
