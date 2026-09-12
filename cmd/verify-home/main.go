package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

// Real provider verification using public scenario text, never microphone data.
func main() {
	suite := flag.String("suite", "home", "Verification suite: home, wait, replacement, praise or switch")
	output := flag.String("output", "", "Optional evidence file path")
	flag.Parse()
	if *suite != "home" && *suite != "wait" && *suite != "replacement" && *suite != "praise" && *suite != "switch" {
		log.Fatal("unknown suite")
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("invalid .env file")
	}
	config := llm.ConfigFromEnv()
	client, err := llm.NewClient(config)
	if err != nil {
		log.Fatal(err)
	}
	type result struct {
		Input     string        `json:"input"`
		Expected  home.Action   `json:"expected"`
		Call      home.Call     `json:"tool_call"`
		LatencyMS int64         `json:"latency_ms"`
		Error     string        `json:"error,omitempty"`
		History   []llm.Message `json:"recent_history,omitempty"`
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
	if *suite == "wait" {
		results = []result{
			{Input: "等一下再去种地", Expected: home.Plant},
			{Input: "等一下，再去种地", Expected: home.Plant},
			{Input: "等会再种菜", Expected: home.Plant},
			{Input: "等一下别浇水了先去种地", Expected: home.Plant},
		}
	}
	if *suite == "replacement" {
		results = nil
		histories := [][]llm.Message{
			nil,
			{{Role: "user", Content: "去浇水。"}},
			{{Role: "user", Content: "去浇水。"}, {Role: "assistant", Content: strings.Repeat("我正在浇水。", 10)},
				{Role: "user", Content: "等一下再去种地。"}, {Role: "assistant", Content: strings.Repeat("我正在种菜。", 10)},
				{Role: "user", Content: "去浇水。"}, {Role: "assistant", Content: strings.Repeat("我正在浇水。", 10)},
				{Role: "user", Content: "你真棒。"}, {Role: "assistant", Content: strings.Repeat("贴贴。", 10)},
				{Role: "user", Content: "去浇水。"}},
		}
		for _, history := range histories {
			for _, input := range []string{"先别浇水了，去施肥。", "先别浇水了，去施肥。", "不用浇水了，改成施肥", "先别浇水了，去种地"} {
				expected := home.Fertilize
				if strings.Contains(input, "种地") {
					expected = home.Plant
				}
				results = append(results, result{Input: input, Expected: expected, History: history})
			}
		}
	}
	if *suite == "praise" {
		results = nil
		histories := [][]llm.Message{
			nil,
			{{Role: "user", Content: "去种地。"}, {Role: "assistant", Content: strings.Repeat("我正在种菜。", 10)}},
			{{Role: "user", Content: "去浇水。"}, {Role: "assistant", Content: strings.Repeat("我正在浇水。", 10)},
				{Role: "user", Content: "浇完水去施肥。"}, {Role: "user", Content: "别施肥了，去种地。"},
				{Role: "assistant", Content: strings.Repeat("我正在种菜。", 10)}},
			{{Role: "user", Content: "干的不错。"}, {Role: "assistant", Content: "小洛克，这次任务没能启动，请再试一次。"}},
		}
		for _, history := range histories {
			for _, input := range []string{"干的不错。", "干得不错。", "干得不错。"} {
				results = append(results, result{Input: input, Expected: home.Affection, History: history})
			}
		}
	}
	if *suite == "switch" {
		results = []result{
			{Input: "去种菜", Expected: home.Plant},
			{Input: "去施肥", Expected: home.Fertilize},
			{Input: "去浇水", Expected: home.Water},
			{Input: "去收菜", Expected: home.Harvest},
			{Input: "一加一等于几", Expected: home.GeneralQA},
			{Input: "先别种菜了，一加一等于几", Expected: home.GeneralQA},
		}
	}
	passed := true
	for i := range results {
		r := &results[i]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		started := time.Now()
		r.Call, err = client.ClassifyAction(ctx, llm.ActionInput{UserText: r.Input, RecentHistory: r.History})
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
		Text      string `json:"text"`
		Tool      string `json:"tool"`
		Expected  bool   `json:"expected"`
		Actual    bool   `json:"actual"`
		IsFinal   bool   `json:"is_final"`
		LatencyMS int64  `json:"latency_ms"`
		Error     string `json:"error,omitempty"`
	}
	intents := []intentResult{{Text: "别浇水了去施肥", Tool: "water", Expected: true}, {Text: "迪莫你真棒", Tool: "fertilize"}, {Text: "浇完水再去施肥", Tool: "water"}, {Text: "不用停你继续施肥", Tool: "fertilize"}}
	for i := range intents {
		intents[i].IsFinal = true
	}
	if *suite == "wait" {
		intents = []intentResult{
			{Text: "等一下再去种地", Tool: "water", IsFinal: true},
			{Text: "等一下，再去种地", Tool: "water", IsFinal: true},
			{Text: "等会儿再去种菜", Tool: "water", IsFinal: true},
			{Text: "等一下", Tool: "water"},
			{Text: "迪莫等一下再", Tool: "water"},
			{Text: "等一下再去种地", Tool: "water"},
			{Text: "等一下", Tool: "water", IsFinal: true, Expected: true},
			{Text: "等一下别浇水了先去种地", Tool: "water", IsFinal: true, Expected: true},
			{Text: "别浇水了等一下再去种地", Tool: "water", IsFinal: true, Expected: true},
		}
	}
	if *suite == "replacement" {
		intents = nil
	}
	if *suite == "praise" {
		intents = []intentResult{{Text: "干的不错。", Tool: "plant", IsFinal: true}, {Text: "干得不错。", Tool: "plant", IsFinal: true}}
	}
	if *suite == "switch" {
		intents = nil
		commands := []struct{ tool, text string }{{"water", "去浇水"}, {"plant", "去种菜"}, {"harvest", "去收菜"}, {"fertilize", "去施肥"}}
		for _, current := range commands {
			for _, next := range commands {
				intents = append(intents, intentResult{Text: next.text, Tool: current.tool, IsFinal: true, Expected: current.tool != next.tool})
			}
		}
		intents = append(intents, []intentResult{
			{Text: "去施肥", Tool: "plant", Expected: true},
			{Text: "去施", Tool: "plant"},
			{Text: "去种地", Tool: "plant", IsFinal: true},
			{Text: "种完菜再去施肥", Tool: "plant", IsFinal: true},
			{Text: "等一下再去施肥", Tool: "plant", IsFinal: true},
			{Text: "去施肥，等种完再去", Tool: "plant", IsFinal: true},
			{Text: "你先继续种菜，等会施肥", Tool: "plant", IsFinal: true},
			{Text: "怎么施肥", Tool: "plant", IsFinal: true},
			{Text: "一加一等于几", Tool: "plant", IsFinal: true},
			{Text: "先别种菜了，一加一等于几", Tool: "plant", IsFinal: true, Expected: true},
			{Text: "先回答我一加一等于几", Tool: "plant", IsFinal: true, Expected: true},
			{Text: "你说的去施肥是什么意思", Tool: "plant", IsFinal: true},
			{Text: "不要去施肥", Tool: "plant", IsFinal: true},
			{Text: "干得不错", Tool: "plant", IsFinal: true},
			{Text: "贴贴", Tool: "plant", IsFinal: true},
			{Text: "去种菜", Tool: "general_qa", IsFinal: true, Expected: true},
			{Text: "去施肥", Tool: "affection", IsFinal: true, Expected: true},
		}...)
	}
	for i := range intents {
		r := &intents[i]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		started := time.Now()
		r.Actual, err = client.ClassifyInterruption(ctx, llm.InterruptionInput{UserText: r.Text, CurrentTool: r.Tool, IsFinal: r.IsFinal})
		r.LatencyMS = time.Since(started).Milliseconds()
		cancel()
		if err != nil {
			r.Error = err.Error()
		}
		if err != nil || r.Actual != r.Expected {
			passed = false
		}
		log.Printf("intent current=%s text=%q final=%t expected=%t actual=%t latency_ms=%d error=%q", r.Tool, r.Text, r.IsFinal, r.Expected, r.Actual, r.LatencyMS, r.Error)
	}
	evidence := map[string]any{"recorded_at": time.Now().UTC().Format(time.RFC3339), "model": config.Model, "passed": passed, "actions": results, "interruptions": intents}
	data, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.MkdirAll("docs/evidence", 0755); err != nil {
		log.Fatal(err)
	}
	path := "docs/evidence/home-classifier.json"
	if *suite == "wait" {
		path = "docs/evidence/home-wait-classifier.json"
	}
	if *suite == "replacement" {
		path = "docs/evidence/home-replacement-classifier.json"
	}
	if *suite == "praise" {
		path = "docs/evidence/home-praise-classifier.json"
	}
	if *suite == "switch" {
		path = "docs/evidence/home-switch-classifier.json"
	}
	if *output != "" {
		path = *output
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(evidence)
	if !passed {
		os.Exit(1)
	}
}
