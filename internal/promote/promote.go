// Package promote resolves environment promotion: a staged chain
// (dev → staging → prod) for environments flagged promote, or fully independent
// per-environment deploys otherwise. Everything is configurable — the operator
// decides which envs are a chain and which stand alone, and each env may
// override the rollout strategy.
package promote

import "go.klarlabs.de/rolloffs/internal/config"

// Chain returns the ordered names of environments that participate in staged
// promotion (promote: true), in config order. Independent environments are not
// part of the chain.
func Chain(c *config.Config) []string {
	var out []string
	for _, e := range c.Spec.Environments {
		if e.Promote {
			out = append(out, e.Name)
		}
	}
	return out
}

// IsStaged reports whether any staged promotion is configured.
func IsStaged(c *config.Config) bool { return len(Chain(c)) > 0 }

// Next returns the environment to promote to after current, and whether one
// exists. If current is not in the chain, the first stage is returned.
func Next(c *config.Config, current string) (string, bool) {
	chain := Chain(c)
	for i, name := range chain {
		if name == current {
			if i+1 < len(chain) {
				return chain[i+1], true
			}
			return "", false // current is the last stage
		}
	}
	if len(chain) > 0 {
		return chain[0], true
	}
	return "", false
}

// Independent returns environments that deploy on their own (not promote-flagged).
func Independent(c *config.Config) []string {
	var out []string
	for _, e := range c.Spec.Environments {
		if !e.Promote {
			out = append(out, e.Name)
		}
	}
	return out
}

// EffectiveStrategy resolves the strategy for an environment: its own override
// if set, else the spec default.
func EffectiveStrategy(c *config.Config, env string) config.Strategy {
	for _, e := range c.Spec.Environments {
		if e.Name == env && e.Strategy != nil {
			return *e.Strategy
		}
	}
	return c.Spec.Strategy
}
