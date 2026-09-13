package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

type testCase struct {
	ID                string        `json:"id"`
	Category          string        `json:"category"`
	Kind              string        `json:"kind"`
	UserText          string        `json:"user_text"`
	CurrentTool       string        `json:"current_tool,omitempty"`
	IsFinal           *bool         `json:"is_final,omitempty"`
	ExpectedAction    home.Action   `json:"expected_action,omitempty"`
	ExpectedPlan      []home.Action `json:"expected_plan,omitempty"`
	ExpectedInterrupt *bool         `json:"expected_interrupt,omitempty"`
	RecentHistory     []llm.Message `json:"recent_history,omitempty"`
	PreviousUserText  string        `json:"previous_user_text,omitempty"`
	AssistantResponse string        `json:"assistant_response,omitempty"`
}

type suite struct {
	Version string     `json:"version"`
	Cases   []testCase `json:"cases"`
}

type record struct {
	ID         string `json:"id"`
	Round      int    `json:"round"`
	Actual     any    `json:"actual"`
	Passed     bool   `json:"passed"`
	Fallback   bool   `json:"fallback"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	Validation string `json:"validation,omitempty"`
}

func readSuite(path string) (suite, error) {
	f, err := os.Open(path)
	if err != nil {
		return suite{}, err
	}
	defer f.Close()
	var s suite
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, errors.New("unexpected trailing suite data")
	}
	if s.Version == "" || len(s.Cases) == 0 {
		return s, errors.New("suite must have a version and cases")
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if c.ID == "" || seen[c.ID] || c.Category == "" || strings.TrimSpace(c.UserText) == "" {
			return s, errors.New("invalid or duplicate case ID, category or text")
		}
		seen[c.ID] = true
		switch c.Kind {
		case "action":
			if !validExpectedPlan(c) || c.ExpectedInterrupt != nil || c.IsFinal != nil {
				return s, fmt.Errorf("invalid action case %s", c.ID)
			}
		case "interrupt":
			if c.ExpectedInterrupt == nil || c.IsFinal == nil || !home.Valid(home.Action(c.CurrentTool)) || c.ExpectedAction != "" || c.ExpectedPlan != nil {
				return s, fmt.Errorf("invalid interruption case %s", c.ID)
			}
		default:
			return s, fmt.Errorf("unknown kind for %s", c.ID)
		}
	}
	return s, nil
}

func validExpectedPlan(c testCase) bool {
	if c.ExpectedPlan == nil {
		return home.Valid(c.ExpectedAction)
	}
	if c.ExpectedAction != "" || len(c.ExpectedPlan) == 0 || len(c.ExpectedPlan) > home.MaxPlanSteps {
		return false
	}
	for _, action := range c.ExpectedPlan {
		if !home.Valid(action) {
			return false
		}
	}
	return true
}

func planResult(plan home.Plan, c testCase) (any, bool) {
	actual := make([]home.Action, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		actual = append(actual, step.Action)
	}
	expected := c.ExpectedPlan
	if expected == nil {
		expected = []home.Action{c.ExpectedAction}
	}
	passed := len(actual) == len(expected)
	if passed {
		for i := range actual {
			if actual[i] != expected[i] {
				passed = false
			}
		}
	}
	if len(actual) == 1 && c.ExpectedPlan == nil {
		return actual[0], passed
	}
	return actual, passed
}

func run() int {
	casesPath := flag.String("cases", "testdata/acceptance/cases.json", "Versioned fixed acceptance set")
	output := flag.String("output", "bin/acceptance-text.json", "Raw result path")
	repeats := flag.Int("repeat", 3, "Rounds, 1 to 20")
	flag.Parse()
	s, err := readSuite(*casesPath)
	if err != nil || *repeats < 1 || *repeats > 20 {
		log.Printf("invalid acceptance configuration: %v", err)
		return 2
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Print("invalid .env file")
		return 2
	}
	cfg := llm.ConfigFromEnv()
	if !cfg.Enabled() {
		log.Print("DeepSeek credentials are not configured")
		return 2
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		log.Print(err)
		return 2
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		log.Print(err)
		return 2
	}
	result := struct {
		Version   string    `json:"version"`
		Model     string    `json:"model"`
		StartedAt time.Time `json:"started_at"`
		Repeat    int       `json:"repeat"`
		Completed bool      `json:"completed"`
		Records   []record  `json:"records"`
	}{Version: s.Version, Model: cfg.Model, StartedAt: time.Now().UTC(), Repeat: *repeats, Records: []record{}}
	save := func() error {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(*output, append(data, '\n'), 0644)
	}
	if err := save(); err != nil {
		log.Print(err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	passed := true
	for round := 1; round <= *repeats; round++ {
		for _, c := range s.Cases {
			if ctx.Err() != nil {
				return 2
			}
			r := record{ID: c.ID, Round: round}
			timeout := 2 * time.Second
			if c.Kind == "action" {
				timeout = 5 * time.Second
			}
			requestCtx, cancel := context.WithTimeout(ctx, timeout)
			started := time.Now()
			// Single calls measure the classifier itself. Runtime retries and
			// partial gating are measured separately by the media scenarios.
			if c.Kind == "action" {
				var plan home.Plan
				plan, err = client.PlanActions(requestCtx, llm.ActionInput{UserText: c.UserText, RecentHistory: c.RecentHistory})
				if err == nil {
					r.Actual, r.Passed = planResult(plan, c)
				}
			} else {
				var decision bool
				decision, err = client.ClassifyInterruption(requestCtx, llm.InterruptionInput{UserText: c.UserText, CurrentTool: c.CurrentTool,
					IsFinal: *c.IsFinal, PreviousUserText: c.PreviousUserText, AssistantResponse: c.AssistantResponse})
				r.Actual = decision
				r.Passed = err == nil && decision == *c.ExpectedInterrupt
				r.Fallback = err != nil
			}
			r.LatencyMS = time.Since(started).Milliseconds()
			cancel()
			if err != nil {
				r.Error = "request_failed"
				if errors.Is(err, context.DeadlineExceeded) {
					r.Error = "timeout"
				} else if errors.Is(err, llm.ErrInvalidActionResult) || errors.Is(err, llm.ErrInvalidIntentResult) {
					r.Error = "invalid_output"
				}
				r.Validation = llm.ActionValidationReason(err)
			}
			passed = passed && r.Passed
			result.Records = append(result.Records, r)
			if err := save(); err != nil {
				log.Print(err)
				return 2
			}
			log.Printf("round=%d case=%s passed=%t latency_ms=%d error=%s", round, c.ID, r.Passed, r.LatencyMS, r.Error)
		}
	}
	result.Completed = true
	if err := save(); err != nil {
		log.Print(err)
		return 2
	}
	if !passed {
		return 1
	}
	return 0
}

func main() { os.Exit(run()) }
