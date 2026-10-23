package auth

import (
	"net/http/httptest"
	"testing"
)

func TestLocalOriginBoundary(t *testing.T) {
	for _, origin := range []string{"http://localhost:18081", "http://127.0.0.1:18081", "http://[::1]:18081", "https://hub.example.test"} {
		h, err := NewHTTP(nil, origin, "", nil)
		if err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
		req := httptest.NewRequest("GET", origin+"/auth/login", nil)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("%s: %d", origin, out.Code)
		}
		cookies := out.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
			t.Fatal("Cookie protection lost")
		}
		req.Host = "attacker.example"
		out = httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatal("Foreign host accepted")
		}
	}
	for _, origin := range []string{"http://hub.example.test", "http://192.168.1.2:18081", "http://localhost.evil.test", "http://localhost:18081/path", "http://user@localhost:18081"} {
		if _, err := NewHTTP(nil, origin, "", nil); err == nil {
			t.Fatalf("Unsafe origin accepted: %s", origin)
		}
	}
}
