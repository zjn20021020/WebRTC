package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

// Real provider verification using public scenario text, never microphone data.
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
		Input     string      `json:"input"`
		Expected  home.Action `json:"expected"`
		Call      home.Call   `json:"tool_call"`
		LatencyMS int64       `json:"latency_ms"`
		Error     string      `json:"error,omitempty"`
	}
	results := []result{
		{Input: "迪莫，你去浇下水", Expected: home.Water},
		{Input: "帮我种菜", Expected: home.Plant},
		{Input: "去收菜", Expected: home.Harvest},
		{Input: "别浇水了去施肥", Expected: home.Fertilize},
		{Input: "迪莫你真棒", Expected: home.Affection},
		{Input: "一加一等于几", Expected: home.GeneralQA},
		{Input: "怎么浇水", Expected: home.GeneralQA},
		{Input: "别浇水", Expected: home.GeneralQA},
		{Input: "先浇水再施肥", Expected: home.GeneralQA},
		{Input: "谢谢，但别贴贴", Expected: home.GeneralQA},
		{Input: "把来偷菜的人踢出去", Expected: home.GeneralQA},
		{Input: "请解释这句话的意思：迪莫去浇水", Expected: home.GeneralQA},
	}
	passed := true
	for i := range results {
		r := &results[i]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		started := time.Now()
		r.Call, err = client.ClassifyAction(ctx, llm.ActionInput{UserText: r.Input})
		r.LatencyMS = time.Since(started).Milliseconds()
		cancel()
		if err != nil {
			r.Error = err.Error()
		}
		if err != nil || r.Call.Name != r.Expected {
			passed = false
		}
		log.Printf("action expected=%s actual=%s latency_ms=%d error=%q", r.Expected, r.Call.Name, r.LatencyMS, r.Error)
	}
	type intentResult struct {
		Text     string `json:"text"`
		Tool     string `json:"tool"`
		Expected bool   `json:"expected"`
		Actual   bool   `json:"actual"`
		Error    string `json:"error,omitempty"`
	}
	intents := []intentResult{{Text: "别浇水了去施肥", Tool: "water", Expected: true}, {Text: "迪莫你真棒", Tool: "fertilize"}, {Text: "浇完水再去施肥", Tool: "water"}, {Text: "不用停你继续施肥", Tool: "fertilize"}}
	for i := range intents {
		r := &intents[i]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r.Actual, err = client.ClassifyInterruption(ctx, llm.InterruptionInput{UserText: r.Text, CurrentTool: r.Tool, IsFinal: true})
		cancel()
		if err != nil {
			r.Error = err.Error()
		}
		if err != nil || r.Actual != r.Expected {
			passed = false
		}
	}
	evidence := map[string]any{"recorded_at": time.Now().UTC().Format(time.RFC3339), "model": config.Model, "passed": passed, "actions": results, "interruptions": intents}
	data, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.MkdirAll("docs/evidence", 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("docs/evidence/home-classifier.json", append(data, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(evidence)
	if !passed {
		os.Exit(1)
	}
}
