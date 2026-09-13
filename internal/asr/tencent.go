package asr

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const endpoint = "wss://asr.cloud.tencent.com"

var ErrAudioBacklog = errors.New("ASR audio backlog exceeded")
var numericID = regexp.MustCompile(`^[0-9]+$`)
var secretID = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// Utterance IDs identify input sentences independently of response epochs.
type Event struct {
	Event           string    `json:"event"`
	Status          string    `json:"status,omitempty"`
	Code            int       `json:"code,omitempty"`
	Model           string    `json:"model,omitempty"`
	Detail          string    `json:"detail,omitempty"`
	Text            string    `json:"text,omitempty"`
	UtteranceID     string    `json:"utterance_id,omitempty"`
	BeginTime       int64     `json:"begin_time,omitempty"`
	EndTime         int64     `json:"end_time,omitempty"`
	SpeechEndAt     time.Time `json:"-"`
	FinalReceivedAt time.Time `json:"-"`
}

type providerEvent struct {
	Code      *int   `json:"code"`
	VoiceID   string `json:"voice_id"`
	Final     int    `json:"final"`
	Sentences *struct {
		List []struct {
			Text      string `json:"sentence"`
			Type      *int   `json:"sentence_type"`
			ID        *int   `json:"sentence_id"`
			StartTime int64  `json:"start_time"`
			EndTime   int64  `json:"end_time"`
		} `json:"sentence_list"`
	} `json:"sentences"`
	Result *struct {
		SliceType int    `json:"slice_type"`
		Index     int    `json:"index"`
		StartTime int64  `json:"start_time"`
		EndTime   int64  `json:"end_time"`
		Text      string `json:"voice_text_str"`
	} `json:"result"`
}

func signedURL(config Config, baseURL, voiceID string, now time.Time, nonce string) (string, error) {
	if !config.Enabled() || !numericID.MatchString(config.AppID) || !secretID.MatchString(config.SecretID) {
		return "", errors.New("invalid Tencent ASR credentials")
	}
	if config.Model != "8k_zh" && config.Model != ModelLargeV2 {
		return "", errors.New("unsupported Tencent ASR model; use 8k_zh or 16k_zh_en_2.0")
	}
	target, err := url.Parse(baseURL)
	if err != nil || target.Host == "" || (target.Scheme != "wss" && target.Scheme != "ws") {
		return "", errors.New("invalid ASR endpoint")
	}
	target.Path = "/asr/v2/" + config.AppID
	parameters := url.Values{
		"secretid":  {config.SecretID},
		"timestamp": {strconv.FormatInt(now.Unix(), 10)},
		"expired":   {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		"nonce":     {nonce}, "engine_model_type": {config.Model},
		"voice_id": {voiceID}, "voice_format": {"1"},
		"needvad": {"1"}, "vad_silence_time": {"600"},
		"filter_dirty": {"0"}, "filter_modal": {"0"}, "filter_punc": {"0"},
		"convert_num_mode": {"1"},
	}
	if config.Model == ModelLargeV2 {
		// V2 accepts our original 8kHz PCM and resamples it in the cloud.
		parameters.Set("input_sample_rate", strconv.Itoa(SampleRate))
		parameters.Set("result_mod", "1")
		parameters.Set("sentence_strategy", "0")
		parameters.Del("vad_silence_time")
		parameters.Del("filter_punc")
	}
	// Tencent signs the sorted query without the scheme. All unsigned values
	// here are ASCII identifiers/numbers, so encoding leaves the raw values intact.
	canonical := target.Host + target.Path + "?" + parameters.Encode()
	signer := hmac.New(sha1.New, []byte(config.SecretKey))
	_, _ = signer.Write([]byte(canonical))
	parameters.Set("signature", base64.StdEncoding.EncodeToString(signer.Sum(nil)))
	target.RawQuery = parameters.Encode()
	return target.String(), nil
}

// Run streams little-endian PCM16, including silence, while receiving results.
// Cancelling closes the socket, including during handshake and blocked reads.
func Run(ctx context.Context, config Config, input <-chan []byte, emit func(Event)) error {
	return run(ctx, config, endpoint, input, emit)
}

func run(ctx context.Context, config Config, baseURL string, input <-chan []byte, emit func(Event)) (resultErr error) {
	defer func() {
		if ctx.Err() != nil {
			resultErr = context.Cause(ctx)
		}
	}()
	voiceID := uuid.NewString()
	nonce, err := rand.Int(rand.Reader, big.NewInt(2147483647))
	if err != nil {
		return errors.New("ASR nonce generation failed")
	}
	nonce.Add(nonce, big.NewInt(1))
	address, err := signedURL(config, baseURL, voiceID, time.Now(), nonce.String())
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
	connection, response, err := dialer.DialContext(ctx, address, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		if response != nil {
			return fmt.Errorf("Tencent ASR handshake failed (HTTP %d)", response.StatusCode)
		}
		// A dial error may include the signed URL; never expose it in logs or UI.
		return errors.New("Tencent ASR WebSocket connection failed")
	}
	defer connection.Close()
	connection.SetReadLimit(1024 * 1024)
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()

	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	var started providerEvent
	if err := connection.ReadJSON(&started); err != nil {
		return errors.New("Tencent ASR did not acknowledge the connection")
	}
	if started.Code == nil || *started.Code != 0 {
		if started.Code != nil {
			return &ProviderError{Code: *started.Code}
		}
		return errors.New("invalid Tencent ASR acknowledgement")
	}
	if started.VoiceID != voiceID {
		return errors.New("Tencent ASR voice ID mismatch")
	}
	_ = connection.SetReadDeadline(time.Time{})
	emit(Event{Event: "asr_status", Status: "listening", Model: config.Model})

	readerDone := make(chan error, 1)
	go func() { readerDone <- readResults(connection, voiceID, emit) }()
	readerReturned := false
	defer func() {
		_ = connection.Close()
		if !readerReturned {
			<-readerDone
		}
	}()

	// Tencent recommends real-time pacing, 3200 bytes per 200ms at 8kHz PCM16.
	const chunkDuration = 200 * time.Millisecond
	const chunkBytes = SampleRate * 2 / 5
	nextWrite := time.Now()
	writeAudio := func(data []byte) error {
		timer := time.NewTimer(time.Until(nextWrite))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-timer.C:
		}
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := connection.WriteMessage(websocket.BinaryMessage, data); err != nil {
			return errors.New("Tencent ASR audio send failed")
		}
		if nextWrite.Before(time.Now().Add(-chunkDuration)) {
			nextWrite = time.Now()
		}
		nextWrite = nextWrite.Add(time.Duration(len(data)) * time.Second / (SampleRate * 2))
		return nil
	}
	pending := make([]byte, 0, chunkBytes)
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case err := <-readerDone:
			readerReturned = true
			if err == nil {
				return errors.New("Tencent ASR ended before the audio stream")
			}
			return err
		case data, open := <-input:
			if !open {
				if len(pending) > 0 {
					if err := writeAudio(pending); err != nil {
						return err
					}
				}
				_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := connection.WriteJSON(map[string]string{"type": "end"}); err != nil {
					return errors.New("Tencent ASR finish request failed")
				}
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case err := <-readerDone:
					readerReturned = true
					return err
				case <-ctx.Done():
					return context.Cause(ctx)
				case <-timer.C:
					return errors.New("Tencent ASR finish timed out")
				}
			}
			if len(data)%2 != 0 {
				return errors.New("ASR requires complete PCM16 samples")
			}
			pending = append(pending, data...)
			for len(pending) >= chunkBytes {
				if err := writeAudio(pending[:chunkBytes]); err != nil {
					return err
				}
				pending = pending[chunkBytes:]
			}
		}
	}
}

func readResults(connection *websocket.Conn, voiceID string, emit func(Event)) error {
	lastFinalID := -1
	for {
		var message providerEvent
		if err := connection.ReadJSON(&message); err != nil {
			return errors.New("Tencent ASR result stream disconnected")
		}
		if message.Code == nil {
			return errors.New("invalid Tencent ASR response")
		}
		if *message.Code != 0 {
			return &ProviderError{Code: *message.Code}
		}
		if message.VoiceID != voiceID {
			continue
		}
		if message.Sentences != nil {
			for _, sentence := range message.Sentences.List {
				if sentence.Text == "" {
					continue
				}
				if sentence.ID == nil || *sentence.ID < 0 || sentence.Type == nil || (*sentence.Type != 0 && *sentence.Type != 1) {
					return errors.New("invalid Tencent ASR V2 sentence")
				}
				// V2 can repeat finalized sentences in subsequent snapshots.
				if *sentence.ID <= lastFinalID {
					continue
				}
				event := "asr_partial"
				if *sentence.Type == 1 {
					event = "asr_final"
					lastFinalID = *sentence.ID
				}
				emit(Event{Event: event, Text: sentence.Text,
					UtteranceID: voiceID + ":" + strconv.Itoa(*sentence.ID),
					BeginTime:   sentence.StartTime, EndTime: sentence.EndTime})
			}
		} else if sentence := message.Result; sentence != nil && sentence.Text != "" {
			event := "asr_partial"
			if sentence.SliceType == 2 {
				event = "asr_final"
			}
			emit(Event{
				Event: event, Text: sentence.Text,
				UtteranceID: voiceID + ":" + strconv.Itoa(sentence.Index),
				BeginTime:   sentence.StartTime, EndTime: sentence.EndTime,
			})
		}
		if message.Final == 1 {
			return nil
		}
	}
}
