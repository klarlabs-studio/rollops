// Package cliapi is the command-line projection of the v2 API (spec 23.1,
// spec 26).
//
// It decides nothing. Everything a command reports — what may be done next,
// whether policy allowed it, what a plan changes — is derived in api/v2 and
// rendered here, because a rule written in the CLI is a rule gRPC, HTTP and MCP
// callers do not have.
//
// What is genuinely the CLI's own is the shape of the answer: an exit code and
// a JSON document, both of which spec 26 makes contract so that a script need
// never read English.
package cliapi

import (
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
)

// ExitCode is the status the process ends with. Spec 26.4 names the categories;
// these are the numbers, and they are contract — a script branching on them is
// the point of having them at all.
type ExitCode int

const (
	// ExitOK is success.
	ExitOK ExitCode = 0

	// ExitValidation means the request was refused before anything was touched
	// and the fix is on the caller's side.
	ExitValidation ExitCode = 2

	// ExitPolicy means the caller was refused. No retry lifts it.
	ExitPolicy ExitCode = 3

	// ExitApprovalRequired means a gate is waiting rather than refusing. It is
	// separate from ExitPolicy because the remedy is an action somebody can go
	// and take.
	ExitApprovalRequired ExitCode = 4

	// ExitVerificationFailed means the checks ran and did not pass.
	ExitVerificationFailed ExitCode = 5

	// ExitDeploymentFailed means the apply itself failed. Whether anything
	// landed is the deployment's timeline to answer.
	ExitDeploymentFailed ExitCode = 6

	// ExitSystem means the system could not be reached, did not answer, or
	// failed in a way nobody classified. The outcome of the operation is
	// unknown.
	ExitSystem ExitCode = 7
)

// exitCode maps a stable code to the status the process ends with.
//
// 1 is deliberately absent. A shell that cannot find the binary, a wrapper that
// gave up and a process killed by a signal all produce it, and an exit we also
// produced could not be told apart from any of those.
//
// Several codes share an exit, which is expected: spec 26.4 names seven
// operational situations and spec 23.4 draws fifteen distinctions, so the exit
// says what to do and the code — in the message, and in --output json — says
// what happened. Which collapse, and why:
//
//   - ExitValidation takes INVALID_ARGUMENT, NOT_FOUND, CONFLICT, PLAN_STALE
//     and CAPABILITY_UNSUPPORTED. All five mean nothing was touched and sending
//     the identical command again will fail the identical way; what differs is
//     which part of it to change — the arguments, the name, the timing, the
//     plan, or the target.
//   - ExitPolicy takes POLICY_DENIED, FORBIDDEN and UNAUTHORIZED. All three
//     refuse the caller rather than report on the work, and none is fixed by
//     retrying: a human has to grant something or configure something. Grouping
//     UNAUTHORIZED here rather than with the system failures is deliberate — an
//     agent that read a missing credential as an outage would retry it forever.
//   - ExitSystem takes TARGET_UNAVAILABLE, CANCELLED, DEADLINE_EXCEEDED and
//     INTERNAL. In all four the caller learns nothing about the operation: it
//     may have landed. The next step is to go and look, which is why they are
//     not mixed in with the exits that do report an outcome.
//
// APPROVAL_REQUIRED does not join ExitPolicy, VERIFICATION_FAILED does not join
// EXECUTION_FAILED, and neither pair may be collapsed later: each names a
// different thing for the operator to do next.
var exitCode = map[apierr.Code]ExitCode{
	apierr.InvalidArgument:       ExitValidation,
	apierr.NotFound:              ExitValidation,
	apierr.Conflict:              ExitValidation,
	apierr.PlanStale:             ExitValidation,
	apierr.CapabilityUnsupported: ExitValidation,

	apierr.PolicyDenied: ExitPolicy,
	apierr.Forbidden:    ExitPolicy,
	apierr.Unauthorized: ExitPolicy,

	apierr.ApprovalRequired: ExitApprovalRequired,

	apierr.VerificationFailed: ExitVerificationFailed,
	apierr.ExecutionFailed:    ExitDeploymentFailed,

	apierr.TargetUnavailable: ExitSystem,
	apierr.Cancelled:         ExitSystem,
	apierr.DeadlineExceeded:  ExitSystem,
	apierr.Internal:          ExitSystem,
}

// Exit reports the status a command ending in err should exit with.
//
// It classifies rather than asking the caller to, so that a command may return
// whatever error it was handed. An error nobody classified exits as a system
// failure on the same reasoning as apierr's INTERNAL default: it is ours, and
// calling it a validation error would send an operator to read their own
// command.
func Exit(err error) ExitCode {
	classified := apierr.From(err)
	if classified == nil {
		return ExitOK
	}
	if code, decided := exitCode[classified.Code]; decided {
		return code
	}
	return ExitSystem
}
