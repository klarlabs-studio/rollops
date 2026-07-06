package notify

import (
	"context"
	"fmt"

	mcpclient "go.klarlabs.de/mcp/client"
)

// Briefkasten queues mail through a briefkasten outbox by calling its
// `email.send` MCP tool. Unlike direct SMTP, delivery is durable: briefkasten
// queues the message, retries failed sends, and exposes status — a better fit
// for best-effort emitters like the engine, where a dropped notifier error
// would otherwise lose the mail.
type Briefkasten struct {
	URL   string // briefkasten MCP base URL (e.g. http://127.0.0.1:8090)
	Token string // optional bearer token
	To    []string
	// Call invokes an MCP tool and reports (isError, text, err). Injectable
	// for tests; nil uses a per-call HTTP client against URL.
	Call func(ctx context.Context, name string, args any) (bool, string, error)
}

func (b Briefkasten) call() func(context.Context, string, any) (bool, string, error) {
	if b.Call != nil {
		return b.Call
	}
	return func(ctx context.Context, name string, args any) (bool, string, error) {
		var opts []mcpclient.HTTPTransportOption
		if b.Token != "" {
			opts = append(opts, mcpclient.WithHTTPHeader("Authorization", "Bearer "+b.Token))
		}
		transport, err := mcpclient.NewHTTPTransport(b.URL, opts...)
		if err != nil {
			return false, "", err
		}
		c := mcpclient.New(transport)
		if _, err := c.Initialize(ctx); err != nil {
			return false, "", err
		}
		defer func() { _ = c.Close() }()
		res, err := c.CallTool(ctx, name, args)
		if err != nil {
			return false, "", err
		}
		var text string
		for _, item := range res.Content {
			if item.Type == "text" {
				text = item.Text
				break
			}
		}
		return res.IsError, text, nil
	}
}

// Notify queues the event as a plain-text mail in the briefkasten outbox.
func (b Briefkasten) Notify(ctx context.Context, e Event) error {
	isErr, text, err := b.call()(ctx, "email.send", map[string]any{
		"to":      b.To,
		"subject": e.Subject(),
		"body":    e.Message(),
	})
	if err != nil {
		return fmt.Errorf("notify: briefkasten: %w", err)
	}
	if isErr {
		return fmt.Errorf("notify: briefkasten: email.send: %s", text)
	}
	return nil
}
