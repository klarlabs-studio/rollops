package deployment

// Status is where a deployment has got to. The vocabulary is fixed by spec 4.8
// and is deliberately the same whatever the target is: an operator reading a
// timeline should not have to know whether the environment is Kubernetes or a
// machine reached over SSH (INV-007).
type Status string

const (
	StatusPlanned          Status = "planned"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusQueued           Status = "queued"
	StatusApplying         Status = "applying"
	StatusVerifying        Status = "verifying"
	StatusPaused           Status = "paused"
	StatusPromoting        Status = "promoting"
	StatusSucceeded        Status = "succeeded"
	StatusFailed           Status = "failed"
	StatusRollingBack      Status = "rolling_back"
	StatusRolledBack       Status = "rolled_back"
	StatusCancelled        Status = "cancelled"
)

// transitions is the whole state machine, in one place, because spec 4.8
// requires transitions to be validated centrally. A status absent from the map
// has no outgoing edges, which is what makes a terminal status terminal and an
// unrecognised one inert.
//
// This is a table rather than a statekit chart. The chart in internal/rollout
// earns its keep because its transitions are guarded by context the machine
// carries; here every guard the spec asks for — approval, policy, risk — is
// decided outside and arrives as a status to move to. A chart would add an
// interpreter to construct on every load and answer the same question.
var transitions = map[Status][]Status{
	StatusPlanned: {
		StatusAwaitingApproval,
		StatusQueued,
		StatusCancelled,
		StatusFailed,
	},
	// The gate. Nothing downstream returns here: an approval that could be
	// re-requested after work had begun would be an approval of something other
	// than what was approved.
	StatusAwaitingApproval: {
		StatusQueued,
		StatusCancelled,
		StatusFailed,
	},
	StatusQueued: {
		StatusApplying,
		StatusCancelled,
		StatusFailed,
	},
	StatusApplying: {
		StatusVerifying,
		StatusPaused,
		StatusRollingBack,
		StatusFailed,
		StatusCancelled,
	},
	StatusVerifying: {
		StatusPromoting,
		StatusSucceeded,
		StatusPaused,
		StatusRollingBack,
		StatusFailed,
	},
	StatusPaused: {
		StatusApplying,
		StatusPromoting,
		StatusRollingBack,
		StatusCancelled,
		StatusFailed,
	},
	StatusPromoting: {
		StatusSucceeded,
		StatusRollingBack,
		StatusFailed,
	},
	// A rollback completes or it fails. It never reports success, because what
	// succeeded was the undoing and recording that as a successful deployment
	// would put a release in an environment's history that never held.
	StatusRollingBack: {
		StatusRolledBack,
		StatusFailed,
	},
}

// terminalStatuses are the ends of the record. They are listed rather than
// derived from the absence of outgoing edges so that a status accidentally left
// out of the table is not silently promoted to an outcome.
var terminalStatuses = map[Status]bool{
	StatusSucceeded:  true,
	StatusFailed:     true,
	StatusRolledBack: true,
	StatusCancelled:  true,
}

func (s Status) String() string { return string(s) }

// Valid reports whether s is a status the machine knows.
func (s Status) Valid() bool {
	if terminalStatuses[s] {
		return true
	}
	_, ok := transitions[s]
	return ok
}

// IsTerminal reports whether the deployment has finished, whatever the outcome.
func (s Status) IsTerminal() bool { return terminalStatuses[s] }

// CanTransitionTo reports whether next is a legal successor of s. An
// unrecognised status has no successors and is nobody's successor, so a typo in
// stored data fails closed rather than unlocking a path.
func (s Status) CanTransitionTo(next Status) bool {
	if !next.Valid() {
		return false
	}
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}
