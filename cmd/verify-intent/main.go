package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"webrtc-interrupt/internal/llm"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("invalid .env file")
	}
	config := llm.ConfigFromEnv()
	client, err := llm.NewClient(config)
	if err != nil {
		log.Fatal(err)
	}
	type result struct {
		Input     string `json:"input"`
		Final     bool   `json:"final"`
		Expected  bool   `json:"expected"`
		Decision  bool   `json:"decision"`
		LatencyMS int64  `json:"latency_ms"`
		Error     string `json:"error,omitempty"`
	}
	results := []result{
		{Input: "停一下，换个问题", Expected: true},
		{Input: "你继续，不用停", Final: true},
		{Input: "讲完以后再告诉我一加一等于几", Final: true},
		{Input: "我刚才听到你说停一下这个词", Final: true},
		{Input: "等一下，你说错了，是明天", Final: true, Expected: true},
		{Input: "嗯", Final: true},
	}
	passed := true
	for i := range results {
		r := &results[i]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		started := time.Now()
		r.Decision, err = client.ClassifyInterruption(ctx, llm.InterruptionInput{PreviousUserText: "请给我讲一个故事", AssistantResponse: "故事才刚刚开始，接下来还会继续讲述。", UserText: r.Input, IsFinal: r.Final})
		r.LatencyMS = time.Since(started).Milliseconds()
		cancel()
		if err != nil {
			r.Error = err.Error()
		}
		if err != nil || r.Decision != r.Expected {
			passed = false
		}
	}
	evidence := struct {
		RecordedAt string   `json:"recorded_at"`
		Model      string   `json:"model"`
		Passed     bool     `json:"passed"`
		Results    []result `json:"results"`
	}{time.Now().UTC().Format(time.RFC3339), config.Model, passed, results}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("docs/evidence", 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("docs/evidence/intent-classifier.json", append(data, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(evidence)
	if !passed {
		os.Exit(1)
	}
}
