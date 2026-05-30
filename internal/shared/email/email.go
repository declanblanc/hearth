// Package email defines the Sender interface used by Hearth for transactional
// mail and provides a Resend-backed implementation plus an in-memory stub for
// tests/development.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Message struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Stub captures messages in memory and never sends anything. Used in
// development (when RESEND_API_KEY is empty) and in tests.
type Stub struct {
	mu   sync.Mutex
	Sent []Message
}

func (s *Stub) Send(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, m)
	return nil
}

// Drain returns and clears the captured messages.
func (s *Stub) Drain() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Sent
	s.Sent = nil
	return out
}

// Resend is a thin client around Resend's HTTP API. Avoids pulling a vendor
// SDK so the binary stays small.
type Resend struct {
	APIKey string
	From   string
	HTTP   *http.Client
}

func NewResend(apiKey, from string) *Resend {
	return &Resend{APIKey: apiKey, From: from, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (r *Resend) Send(ctx context.Context, m Message) error {
	if r.APIKey == "" {
		return errors.New("email: Resend API key not configured")
	}
	body, err := json.Marshal(map[string]any{
		"from":    r.From,
		"to":      []string{m.To},
		"subject": m.Subject,
		"text":    m.TextBody,
		"html":    m.HTMLBody,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("email: resend status %d: %s", resp.StatusCode, b)
	}
	return nil
}
