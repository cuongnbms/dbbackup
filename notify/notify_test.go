package notify

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// capture starts a server that records the decoded JSON body of every request.
func capture(t *testing.T, status int) (*httptest.Server, func() []map[string]string) {
	t.Helper()
	var mu sync.Mutex
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("body is not a JSON object: %s", body)
		}
		mu.Lock()
		got = append(got, payload)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		result := make([]map[string]string, len(got))
		copy(result, got)
		return result
	}
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
	payloads := got()
	if len(payloads) != 1 || payloads[0]["text"] != "T\nB" {
		t.Fatalf("unexpected payload: %+v", payloads)
	}
}

func TestDiscordSenderUsesContentField(t *testing.T) {
	srv, got := capture(t, 200)
	s := newWebhookSender("discord", srv.URL, "content", srv.Client())
	if err := s.Send(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	payloads := got()
	if len(payloads) != 1 || payloads[0]["content"] != "T\nB" {
		t.Fatalf("unexpected payload: %+v", payloads)
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
	payloads := got()
	if len(payloads) != 0 {
		t.Fatalf("disabled event was delivered: %+v", payloads)
	}
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T"})
	payloads = got()
	if len(payloads) != 1 {
		t.Fatalf("enabled event was not delivered: %+v", payloads)
	}
}

func TestNotifierTruncatesLongBody(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: strings.Repeat("x", 5000)})
	payloads := got()
	text := payloads[0]["text"]
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
	payloads := got()
	text := payloads[0]["text"]
	if strings.Contains(text, "hunter2") {
		t.Fatalf("secret leaked to the webhook: %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Fatalf("expected a redaction marker: %q", text)
	}
}

func TestNotifierKeepsGoingWhenASenderFails(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	bad, _ := capture(t, 500)
	good, got := capture(t, 200)
	n := New([]Sender{
		newWebhookSender("slack", bad.URL, "text", bad.Client()),
		newWebhookSender("discord", good.URL, "content", good.Client()),
	}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T"})
	payloads := got()
	if len(payloads) != 1 {
		t.Fatalf("a failing sender stopped the next one: %+v", payloads)
	}
}

func TestNotifierWithNoSecretDoesNotRedactEverything(t *testing.T) {
	srv, got := capture(t, 200)
	n := New([]Sender{newWebhookSender("slack", srv.URL, "text", srv.Client())}, []string{"backup_failed"}, "")
	n.Notify(context.Background(), Message{Event: BackupFailed, Title: "T", Body: "plain body"})
	payloads := got()
	if payloads[0]["text"] != "T\nplain body" {
		t.Fatalf("unexpected payload: %+v", payloads)
	}
}

func TestSendersFromEnv(t *testing.T) {
	t.Run("neither set", func(t *testing.T) {
		t.Setenv("SLACK_WEBHOOK_URL", "")
		t.Setenv("DISCORD_WEBHOOK_URL", "")
		senders := SendersFromEnv()
		if len(senders) != 0 {
			t.Fatalf("expected no senders, got %d: %+v", len(senders), senders)
		}
	})

	t.Run("only slack set", func(t *testing.T) {
		t.Setenv("SLACK_WEBHOOK_URL", "https://example.com/slack")
		t.Setenv("DISCORD_WEBHOOK_URL", "")
		senders := SendersFromEnv()
		if len(senders) != 1 {
			t.Fatalf("expected exactly one sender, got %d: %+v", len(senders), senders)
		}
		if senders[0].Name() != "slack" {
			t.Fatalf("expected the slack sender, got %q", senders[0].Name())
		}
	})

	t.Run("both set", func(t *testing.T) {
		t.Setenv("SLACK_WEBHOOK_URL", "https://example.com/slack")
		t.Setenv("DISCORD_WEBHOOK_URL", "https://example.com/discord")
		senders := SendersFromEnv()
		if len(senders) != 2 {
			t.Fatalf("expected exactly two senders, got %d: %+v", len(senders), senders)
		}
		names := []string{senders[0].Name(), senders[1].Name()}
		if names[0] != "slack" || names[1] != "discord" {
			t.Fatalf("expected [slack discord], got %+v", names)
		}
	})
}

func TestSenderDoesNotLeakWebhookTokenInError(t *testing.T) {
	const fakeToken = "xoxb_supersecrettoken_12345"
	s := newWebhookSender("slack", "http://127.0.0.1:1/services/T00000000/B00000000/"+fakeToken, "text", &http.Client{Timeout: 1 * time.Second})
	err := s.Send(context.Background(), Message{Title: "T"})
	if err == nil {
		t.Fatal("expected an error for unreachable host")
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("webhook token leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Fatalf("sender name should be in error: %v", err)
	}
}
