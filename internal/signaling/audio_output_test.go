package signaling

import (
	"net/http/httptest"
	"strings"
	"testing"

	"webrtc-interrupt/internal/audiooutput"
)

func TestAudioOutputOnlyReadsLocalDevice(t *testing.T) {
	for _, tc := range []struct {
		address, method string
		want            int
	}{
		{"127.0.0.1:1234", "GET", 200}, {"[::1]:1234", "GET", 200},
		{"192.168.1.2:1234", "GET", 403}, {"bad", "GET", 403}, {"127.0.0.1:1234", "POST", 405},
	} {
		t.Run(tc.address+tc.method, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/audio-output", nil)
			r.RemoteAddr = tc.address
			w := httptest.NewRecorder()
			calls := 0
			audioOutput(w, r, func() audiooutput.Route {
				calls++
				return audiooutput.Route{Kind: "headphones", Source: "test"}
			})
			if w.Code != tc.want || (calls == 1) != (tc.want == 200) {
				t.Fatalf("status=%d lookup calls=%d", w.Code, calls)
			}
			if tc.want == 200 && !strings.Contains(w.Body.String(), `"kind":"headphones"`) {
				t.Fatal("missing output route")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("device information must not be cached")
			}
		})
	}
}
