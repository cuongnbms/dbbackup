// Package notify delivers backup notifications to chat webhooks. It knows
// nothing about backups: callers hand it a finished Message.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Event is a named thing that happened during a backup run and that a user can
// subscribe to.
type Event string

const (
	BackupFailed        Event = "backup_failed"
	UploadFailed        Event = "upload_failed"
	RemoteCleanupFailed Event = "remote_cleanup_failed"
	BackupSucceeded     Event = "backup_succeeded"
)

// maxBody caps the message body. Discord rejects payloads over 2000 characters
// and a failed pg_dump returns its whole output, which can exceed that and
// lose the entire alert.
const maxBody = 1500

// RequestTimeout bounds one webhook call. Notification must never hold up a
// backup run. Callers sizing a budget across multiple calls (for example, one
// event delivered to several senders) should derive it from this constant
// rather than repeating the duration.
const RequestTimeout = 10 * time.Second

// Valid reports whether name is an event a user may subscribe to.
func Valid(name string) bool {
	switch Event(name) {
	case BackupFailed, UploadFailed, RemoteCleanupFailed, BackupSucceeded:
		return true
	}
	return false
}

// Message is one notification, already formatted by the caller.
type Message struct {
	Event Event
	Title string
	Body  string
}

// Sender delivers a Message to one destination.
type Sender interface {
	Send(ctx context.Context, msg Message) error
	Name() string
}

// webhookSender posts {"<field>": "<text>"} to a chat webhook. Slack and
// Discord differ only in that field name, so they are one implementation.
type webhookSender struct {
	name   string
	url    string
	field  string
	client *http.Client
}

func newWebhookSender(name, url, field string, client *http.Client) Sender {
	return webhookSender{name: name, url: url, field: field, client: client}
}

func (s webhookSender) Name() string { return s.name }

func (s webhookSender) Send(ctx context.Context, msg Message) error {
	text := msg.Title
	if msg.Body != "" {
		text += "\n" + msg.Body
	}
	payload, err := json.Marshal(map[string]string{s.field: text})
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", s.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("build %s request: %w", s.name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("post to %s: %w", s.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s webhook returned %s", s.name, resp.Status)
	}
	return nil
}

// SendersFromEnv returns one sender per webhook URL present in the
// environment. A channel is active exactly when its URL is set, matching how
// ENCRYPT_KEY switches encryption on.
func SendersFromEnv() []Sender {
	client := &http.Client{Timeout: RequestTimeout}
	var senders []Sender
	if url := os.Getenv("SLACK_WEBHOOK_URL"); url != "" {
		senders = append(senders, newWebhookSender("slack", url, "text", client))
	}
	if url := os.Getenv("DISCORD_WEBHOOK_URL"); url != "" {
		senders = append(senders, newWebhookSender("discord", url, "content", client))
	}
	return senders
}

// Notifier delivers enabled events to every configured sender, redacting a
// secret and truncating long bodies on the way out.
type Notifier struct {
	senders []Sender
	enabled map[Event]bool
	secret  string
}

// New builds a Notifier. events holds the raw names from the config file;
// unknown names are ignored here because Config.validate already rejects them.
// secret is removed from every body before sending.
func New(senders []Sender, events []string, secret string) *Notifier {
	enabled := make(map[Event]bool, len(events))
	for _, e := range events {
		enabled[Event(e)] = true
	}
	return &Notifier{senders: senders, enabled: enabled, secret: secret}
}

// Notify delivers msg if its event is enabled. It returns nothing: a delivery
// failure is logged and discarded, because a notification must never fail a
// backup.
func (n *Notifier) Notify(ctx context.Context, msg Message) {
	if !n.enabled[msg.Event] {
		return
	}
	msg.Body = truncate(redact(msg.Body, n.secret))
	for _, s := range n.senders {
		if err := s.Send(ctx, msg); err != nil {
			log.Printf("Warning: %s notification failed: %v", s.Name(), err)
		}
	}
}

// redact removes secret from body. This is egress to the public internet, so
// it runs even though today's error strings are not known to carry a password.
func redact(body, secret string) string {
	if secret == "" {
		return body
	}
	return strings.ReplaceAll(body, secret, "***")
}

func truncate(body string) string {
	if len(body) <= maxBody {
		return body
	}
	return body[:maxBody] + "\n… (truncated)"
}
