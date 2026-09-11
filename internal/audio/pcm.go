package audio

import "encoding/binary"

// PCM16LE serializes decoded samples without a WAV header.
func PCM16LE(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}
