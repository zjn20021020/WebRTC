package asr

import (
	"os"
	"strings"
)

const SampleRate = 8000

const ModelLargeV2 = "16k_zh_en_2.0"

type Config struct {
	AppID     string
	SecretID  string
	SecretKey string
	Model     string
}

func ConfigFromEnv() Config {
	model := strings.TrimSpace(os.Getenv("TENCENT_ASR_MODEL"))
	if model == "" {
		model = "8k_zh"
	}
	return Config{
		AppID:     strings.TrimSpace(os.Getenv("TENCENT_APP_ID")),
		SecretID:  strings.TrimSpace(os.Getenv("TENCENT_SECRET_ID")),
		SecretKey: strings.TrimSpace(os.Getenv("TENCENT_SECRET_KEY")),
		Model:     model,
	}
}

func (c Config) Enabled() bool {
	return c.AppID != "" && c.SecretID != "" && c.SecretKey != ""
}
