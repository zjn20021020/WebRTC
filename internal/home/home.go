package home

import (
	"context"
	_ "embed"
	"errors"
)

//go:embed skills/dimo/SKILL.md
var RoleSkill string

type Action string

const (
	Water             Action = "water"
	Plant             Action = "plant"
	Harvest           Action = "harvest"
	Fertilize         Action = "fertilize"
	Affection         Action = "affection"
	GeneralQA         Action = "general_qa"
	ActionRepetitions        = 10
)

type Definition struct {
	Name        Action
	Description string
	Speech      string
}

func Definitions() []Definition {
	return []Definition{
		{Water, "明确要求给家园作物浇水；不含否定、询问方法或状态", "我正在浇水。"},
		{Plant, "明确要求在家园种菜、种地、种田或播种", "我正在种菜。"},
		{Harvest, "明确要求收菜、收获家园作物", "我正在收菜。"},
		{Fertilize, "明确要求给家园作物施肥", "我正在施肥。"},
		{Affection, "对迪莫的鼓励、夸赞或明确亲密互动请求；尊重拒绝亲密动作", "贴贴。"},
		{GeneralQA, "知识问答、闲聊、停止确认、不支持的操作或需要澄清的请求", ""},
	}
}

func Valid(action Action) bool {
	for _, definition := range Definitions() {
		if definition.Name == action {
			return true
		}
	}
	return false
}

type Call struct {
	ID   string `json:"id"`
	Name Action `json:"name"`
}

// Epoch is the cancellation/ownership boundary. A future game adapter must
// honor ctx and use Call.ID for idempotency before changing game state.
type Request struct {
	Call     Call
	Epoch    uint64
	UserText string
}

type Executor interface {
	Execute(context.Context, Request, func(string) error) error
}

// VoiceExecutor simulates actions; speech is consumed by the shared turn TTS
// pipeline. Returning only ends production, not playback or the turn lifecycle.
type VoiceExecutor struct{}

func (VoiceExecutor) Execute(ctx context.Context, request Request, speak func(string) error) error {
	for _, definition := range Definitions() {
		if definition.Name != request.Call.Name || definition.Speech == "" {
			continue
		}
		for i := 0; i < ActionRepetitions; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := speak(definition.Speech); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unsupported home action")
}
