package tts

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerr "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tts/v20190823"
	"webrtc-interrupt/internal/audio"
)

const SampleRate = 8000

type Config struct {
	AppID               string
	SecretID, SecretKey string
	VoiceType           int64
}

func ConfigFromEnv() (Config, error) {
	c := Config{AppID: strings.TrimSpace(os.Getenv("TENCENT_APP_ID")), SecretID: strings.TrimSpace(os.Getenv("TENCENT_SECRET_ID")), SecretKey: strings.TrimSpace(os.Getenv("TENCENT_SECRET_KEY")), VoiceType: 101016}
	if value := strings.TrimSpace(os.Getenv("TENCENT_TTS_VOICE_TYPE")); value != "" {
		var err error
		c.VoiceType, err = strconv.ParseInt(value, 10, 64)
		if err != nil || c.VoiceType <= 0 {
			return Config{}, errors.New("TENCENT_TTS_VOICE_TYPE must be a positive voice ID")
		}
	}
	return c, nil
}

func (c Config) Enabled() bool { return c.AppID != "" && c.SecretID != "" && c.SecretKey != "" }

type Client struct {
	config   Config
	api      *tencent.Client
	endpoint string
}

func NewClient(config Config) (*Client, error) {
	p := profile.NewClientProfile()
	p.HttpProfile.ReqTimeout = 20
	api, err := tencent.NewClient(common.NewCredential(config.SecretID, config.SecretKey), "", p)
	if err != nil {
		return nil, errors.New("Tencent TTS client initialization failed")
	}
	return &Client{config: config, api: api, endpoint: streamEndpoint}, nil
}

// Synthesize returns 8kHz mono PCMU without a file header.
func (c *Client) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if !c.config.Enabled() {
		return nil, errors.New("Tencent TTS credentials are not configured")
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 100 {
		return nil, errors.New("TTS segment must contain 1 to 100 characters")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req := tencent.NewTextToVoiceRequest()
	req.Text = common.StringPtr(text)
	req.SessionId = common.StringPtr(uuid.NewString())
	req.VoiceType = common.Int64Ptr(c.config.VoiceType)
	req.ModelType = common.Int64Ptr(1)
	req.PrimaryLanguage = common.Int64Ptr(1)
	req.SampleRate = common.Uint64Ptr(SampleRate)
	req.Codec = common.StringPtr("pcm")
	resp, err := c.api.TextToVoiceWithContext(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var cloudErr *tcerr.TencentCloudSDKError
		if errors.As(err, &cloudErr) {
			return nil, fmt.Errorf("Tencent TTS failed (code %s)", cloudErr.GetCode())
		}
		return nil, errors.New("Tencent TTS connection failed")
	}
	if resp == nil || resp.Response == nil || resp.Response.Audio == nil || resp.Response.SessionId == nil || *resp.Response.SessionId != *req.SessionId {
		return nil, errors.New("invalid Tencent TTS response")
	}
	encoded := *resp.Response.Audio
	if len(encoded) > 2*1024*1024 {
		return nil, errors.New("Tencent TTS audio exceeds segment limit")
	}
	pcm, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(pcm) == 0 || len(pcm)%2 != 0 || bytesAreWAV(pcm) {
		return nil, errors.New("Tencent TTS returned invalid PCM16 audio")
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	return audio.EncodePCMU(samples), nil
}

func bytesAreWAV(data []byte) bool {
	return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
}
