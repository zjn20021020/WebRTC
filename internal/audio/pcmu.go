package audio

import "math"

// DecodePCMU converts G.711 mu-law bytes to signed 16-bit PCM samples.
func DecodePCMU(encoded []byte) []int16 {
	decoded := make([]int16, len(encoded))
	for i, value := range encoded {
		decoded[i] = decodeSample(value)
	}
	return decoded
}

// EncodePCMU converts signed 16-bit PCM samples to G.711 mu-law bytes.
func EncodePCMU(samples []int16) []byte {
	encoded := make([]byte, len(samples))
	for i, sample := range samples {
		encoded[i] = encodeSample(sample)
	}
	return encoded
}

// GenerateTestToneFrames creates a short two-tone PCM stream, split into
// RTP-sized frames for validating the outbound audio path without TTS.
func GenerateTestToneFrames(sampleRate, frameSamples int) [][]byte {
	if sampleRate <= 0 || frameSamples <= 0 {
		return nil
	}
	totalSamples := sampleRate * 2
	frames := make([][]byte, 0, (totalSamples+frameSamples-1)/frameSamples)
	for offset := 0; offset < totalSamples; offset += frameSamples {
		count := frameSamples
		if remaining := totalSamples - offset; remaining < count {
			count = remaining
		}
		pcm := make([]int16, count)
		for i := range pcm {
			t := offset + i
			frequency := 440.0
			if t >= sampleRate {
				frequency = 660.0
			}
			pcm[i] = int16(9000 * math.Sin(2*math.Pi*frequency*float64(t)/float64(sampleRate)))
		}
		frames = append(frames, EncodePCMU(pcm))
	}
	return frames
}

func decodeSample(value byte) int16 {
	value = ^value
	linear := ((int(value)&0x0f)<<3 + 0x84) << uint((value&0x70)>>4)
	if value&0x80 != 0 {
		return int16(0x84 - linear)
	}
	return int16(linear - 0x84)
}

func encodeSample(sample int16) byte {
	const (
		bias = 0x84
		clip = 32635
	)

	value := int(sample)
	sign := 0
	if value < 0 {
		sign = 0x80
		value = -value
	}
	if value > clip {
		value = clip
	}
	value += bias

	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := (value >> (exponent + 3)) & 0x0f
	return ^byte(sign | exponent<<4 | mantissa)
}

// RMS returns the root-mean-square amplitude of PCM samples.
func RMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}
