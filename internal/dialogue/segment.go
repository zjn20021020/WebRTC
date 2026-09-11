package dialogue

import "strings"

type segmenter struct{ pending []rune }

func (s *segmenter) push(text string, emit func(string) error) error {
	for _, r := range text {
		s.pending = append(s.pending, r)
		if len(s.pending) >= 100 || strings.ContainsRune("\u3002\uff01\uff1f!?;\uff1b\n", r) || (len(s.pending) >= 20 && strings.ContainsRune(",\uff0c", r)) {
			if err := s.flush(emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *segmenter) flush(emit func(string) error) error {
	text := strings.TrimSpace(string(s.pending))
	s.pending = nil
	if text == "" {
		return nil
	}
	return emit(text)
}
