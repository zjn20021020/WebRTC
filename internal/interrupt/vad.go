package interrupt

import "time"

type Event string

const (
	SpeechStarted Event = "speech_started"
	SpeechEnded   Event = "speech_ended"
)

// Detector applies consecutive-frame hysteresis to PCM RMS values.
type Detector struct {
	threshold   float64
	startFrames int
	stopFrames  int

	inSpeech     bool
	voiceCount   int
	silenceCount int
}

func NewDetector(threshold float64, startAfter, stopAfter, frameDuration time.Duration) *Detector {
	if threshold < 0 {
		threshold = 0
	}
	if frameDuration <= 0 {
		frameDuration = 20 * time.Millisecond
	}
	return &Detector{
		threshold:   threshold,
		startFrames: durationFrames(startAfter, frameDuration),
		stopFrames:  durationFrames(stopAfter, frameDuration),
	}
}

func durationFrames(duration, frame time.Duration) int {
	if duration <= 0 {
		return 1
	}
	frames := int((duration + frame - 1) / frame)
	if frames < 1 {
		return 1
	}
	return frames
}

func (d *Detector) Update(rms float64) (Event, bool) {
	if rms >= d.threshold {
		d.silenceCount = 0
		d.voiceCount++
		if !d.inSpeech && d.voiceCount >= d.startFrames {
			d.inSpeech = true
			return SpeechStarted, true
		}
		return "", false
	}

	d.voiceCount = 0
	if !d.inSpeech {
		return "", false
	}
	d.silenceCount++
	if d.silenceCount >= d.stopFrames {
		d.inSpeech = false
		return SpeechEnded, true
	}
	return "", false
}

func (d *Detector) InSpeech() bool {
	return d.inSpeech
}
