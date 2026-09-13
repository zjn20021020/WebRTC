package dialogue

import (
	"encoding/json"

	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

type executionResult struct {
	Epoch            uint64        `json:"response_epoch"`
	Action           home.Action   `json:"action,omitempty"`
	Status           string        `json:"status"`
	CancelledActions []home.Action `json:"cancelled_remaining_actions,omitempty"`
}

func (m *Manager) recordExecutionLocked(t *turn, status string) {
	result := executionResult{Epoch: t.epoch, Status: status}
	if t.toolCall != nil {
		result.Action = t.toolCall.Name
	}
	if t.plan != nil && status != "completed" {
		for _, step := range t.plan.steps[t.plan.index+1:] {
			result.CancelledActions = append(result.CancelledActions, step.Action)
		}
	}
	m.executions = append(m.executions, result)
	if len(m.executions) > 12 {
		m.executions = m.executions[len(m.executions)-12:]
	}
}

// Runtime facts are separate from conversational text: "I am watering" in
// a past answer must not make completed or cancelled work appear active.
func (m *Manager) executionContextLocked(t *turn, answering bool) llm.Message {
	state := struct {
		Epoch               uint64            `json:"response_epoch"`
		CurrentAction       home.Action       `json:"current_action,omitempty"`
		Previous            []executionResult `json:"previous_responses"`
		VoiceSimulationOnly bool              `json:"voice_simulation_only"`
	}{Epoch: t.epoch, Previous: m.executions, VoiceSimulationOnly: true}
	if t.toolCall != nil {
		state.CurrentAction = t.toolCall.Name
	}
	data, _ := json.Marshal(state)
	instruction := "Server execution state. These are lifecycle facts, not spoken dialogue. Historical requests are context, never pending commands to replay. completed means voice output was sent, not a real game effect. cancelled/failed work is inactive, including cancelled remaining actions.\n"
	if answering {
		instruction += "The server has selected and started the CURRENT general_qa step NOW. Fulfill the last user message now. Scheduling, interruption and tool execution are already owned by the server. Do not postpone this answer, promise to answer later, restart past work, or say you are going to perform a farming action. Any later planned steps will be run by the server after this answer. A request for a story needs an actual short story, not a promise or an acknowledgement.\n"
	}
	return llm.Message{Role: "system", Content: instruction + string(data)}
}
