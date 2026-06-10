package featureflags

import (
	"context"
	"fmt"
)

type Change struct {
	Flag        string
	Environment string
	Percentage  int
	Disabled    bool
}

type Provider interface {
	ApplyFlag(ctx context.Context, c Change) error
}

type Hook struct {
	Provider Provider
}

func (h Hook) Apply(ctx context.Context, c Change) error {
	if h.Provider == nil {
		return nil
	}
	if c.Flag == "" {
		return fmt.Errorf("featureflags: flag is required")
	}
	if c.Percentage < 0 || c.Percentage > 100 {
		return fmt.Errorf("featureflags: percentage must be between 0 and 100")
	}
	return h.Provider.ApplyFlag(ctx, c)
}
