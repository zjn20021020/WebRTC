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

func decodeSample(value byte) int16 {
	value = ^value
	linear := ((int(value)&0x0f)<<3 + 0x84) << uint((value&0x70)>>4)
	if value&0x80 != 0 {
		return int16(0x84 - linear)
	}
	return int16(linear - 0x84)
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
