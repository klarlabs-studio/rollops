package notify

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// Email sends notifications over SMTP. The server address, sender, and
// recipients come from configuration; credentials are secrets supplied at
// runtime. Send is injectable for tests and defaults to smtp.SendMail, which
// upgrades to STARTTLS when the server supports it.
type Email struct {
	Addr string // host:port
	From string
	To   []string
	Auth smtp.Auth // optional
	Send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

func (m Email) send() func(string, smtp.Auth, string, []string, []byte) error {
	if m.Send != nil {
		return m.Send
	}
	return smtp.SendMail
}

// Notify mails the event as a plain-text message.
func (m Email) Notify(_ context.Context, e Event) error {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", m.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(m.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", e.Subject())
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(e.Message())
	b.WriteString("\r\n")

	if err := m.send()(m.Addr, m.Auth, m.From, m.To, []byte(b.String())); err != nil {
		return fmt.Errorf("notify: email: %w", err)
	}
	return nil
}
