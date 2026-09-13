package signaling

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserDiagnosticsRejectsNonlocalAndUnlistedData(t *testing.T) {
	for _, test := range []struct {
		address, body string
		status        int
	}{
		{"127.0.0.1:12345", `{"session":"test","context":"running","track":"live","rms":0.1,"echo_cancellation":false,"noise_suppression":false,"auto_gain_control":false,"page_visible":true,"playback":true}`, 204},
		{"[::1]:12345", `{}`, 204},
		{"192.168.0.2:12345", `{}`, 403},
		{"127.0.0.1:12345", `{"transcript":"should not be logged"}`, 400},
		{"127.0.0.1:12345", `{"session":"` + strings.Repeat("a", 2048) + `"}`, 400},
	} {
		request := httptest.NewRequest("POST", "/api/diagnostics", strings.NewReader(test.body))
		request.RemoteAddr = test.address
		response := httptest.NewRecorder()
		BrowserDiagnostics(response, request)
		if response.Code != test.status {
			t.Fatalf("got %d, want %d", response.Code, test.status)
		}
	}
}
