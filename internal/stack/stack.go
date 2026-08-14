// Package stack pins the Klarlatz/OSS components Rollops is assembled from
// (TDD §2 Stack Mapping). The blank imports keep these modules as direct
// dependencies and prove the whole stack resolves and compiles together
// before any package wires them in. As each build phase consumes a component
// for real, its import moves out of here into the consuming package.
//
//	statekit    rollout lifecycle statechart
//	axi         step execution kernel (imported as go.klarlabs.de/axi)
//	fortify     resilience: retry / circuit-breaker / rate-limit / bulkhead
//	decisionkit reserved pin (commitment/deadline risk; blast-radius is internal/risk)
//	bolt        structured, compliance-grade audit + event logging
//	mcp         MCP server for the agent surface (imported as go.klarlabs.de/mcp)
//	mnemos      reserved pin (not a Store backend)
package stack

import (
	_ "github.com/felixgeelhaar/decisionkit/risk"
	_ "go.klarlabs.de/axi"
	_ "go.klarlabs.de/bolt"
	_ "go.klarlabs.de/fortify"
	_ "go.klarlabs.de/mcp"
	_ "go.klarlabs.de/mnemos"
	_ "go.klarlabs.de/statekit"
)
