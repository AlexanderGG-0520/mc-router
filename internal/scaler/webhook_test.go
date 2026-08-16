package scaler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotifyPostsEventAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Fatal("missing header")
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := New(Config{Enabled: true, URL: server.URL, Timeout: time.Second, Headers: map[string]string{"Authorization": "Bearer test"}})
	if err := client.Notify(context.Background(), Event{Backend: "hub:25565"}); err != nil {
		t.Fatal(err)
	}
}
