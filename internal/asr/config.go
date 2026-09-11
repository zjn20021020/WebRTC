package asr

import (
	"os"
	"strings"
)

const SampleRate = 8000

type Config struct {
	AppID     string
	SecretID  string
	SecretKey string
	Model     string
}

func ConfigFromEnv() Config {
	return Config{
		AppID:     strings.TrimSpace(os.Getenv("TENCENT_APP_ID")),
		SecretID:  strings.TrimSpace(os.Getenv("TENCENT_SECRET_ID")),
		SecretKey: strings.TrimSpace(os.Getenv("TENCENT_SECRET_KEY")),
		Model:     "8k_zh",
	}
}

func (c Config) Enabled() bool {
	return c.AppID != "" && c.SecretID != "" && c.SecretKey != ""
}
