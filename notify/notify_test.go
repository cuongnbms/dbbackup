package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// capture starts a server that records the decoded JSON body of every request.
func capture(t *testing.T, status int) (*httptest.Server, *[]map[string]string) {
	t.Helper()
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("body is not a JSON object: %s", body)
		}
		got = append(got, payload)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestValidRejectsUnknownEvent(t *testing.T) {
	if !Valid("backup_failed") {
		t.Fatal("backup_failed must be valid")
	}
	if Valid("backup_faild") {
		t.Fatal("a typo must not be accepted")
	}
}

func TestSlackSenderUsesTextField(t *testing.T) {
	srv, got := capture(t, 200)
	s := newWebhookSender("slack", srv.URL, "text", srv.Client())
	if err := s.Send(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0]["text"] != "T\nB" {
		t.Fatalf("unexpected payload: %+v", *got)
	}
}

func TestDiscordSenderUsesContentField(t *testing.T) {
	srv, got := capture(t, 200)
	s := newWebhookSender("discord", srv.URL, "content", srv.Client())
	if err := s.Send(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0]["content"] != "T\nB" {
		t.Fatalf("unexpected payload: %+v", *got)
	}
}

func TestSenderTreatsNon2xxAsError(t *testing.T) {
	srv, _ := capture(t, 500)
	s := newWebhookSender("slack", srv.URL, "text", srv.Client())
	if err := s.Send(context.Background(), Message{Title: "T"}); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestSenderRespectsClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	s := newWebhookSender("slack", srv.URL, "text", &http.Client{Timeout: 30 * time.Millisecond})
	if err := s.Send(context.Background(), Message{Title: "T"}); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestNotifierSkipsDisabledEvent(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupSucceeded, Title: "T"})
	if len(*got) != 0 {
		t.Fatalf("disabled event was delivered: %+v", *got)
	}
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T"})
	if len(*got) != 1 {
		t.Fatalf("enabled event was not delivered: %+v", *got)
	}
}

func TestNotifierTruncatesLongBody(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: strings.Repeat("x", 5000)})
	text := (*got)[0]["text"]
	if len(text) > maxBody+100 {
		t.Fatalf("body was not truncated: %d characters", len(text))
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("truncation should be visible in the message: %q", text[len(text)-60:])
	}
}

func TestNotifierRedactsSecret(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "hunter2")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "dsn=postgres://u:hunter2@db"})
	text := (*got)[0]["text"]
	if strings.Contains(text, "hunter2") {
		t.Fatalf("secret leaked to the webhook: %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Fatalf("expected a redaction marker: %q", text)
	}
}

func TestNotifierKeepsGoingWhenASenderFails(t *testing.T) {
	bad, _ := capture(t, 500)
	good, got := capture(t, 200)
	n := New([]Sender{
		newWebhookSender("slack", bad.URL, "text", bad.Client()),
		newWebhookSender("discord", good.URL, "content", good.Client()),
	}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T"})
	if len(*got) != 1 {
		t.Fatalf("a failing sender stopped the next one: %+v", *got)
	}
}

func TestNotifierWithNoSecretDoesNotRedactEverything(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "plain body"})
	if (*got)[0]["text"] != "T\nplain body" {
		t.Fatalf("unexpected payload: %+v", *got)
	}
}
