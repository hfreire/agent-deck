package session

// HistoricalSubstate retains diagnostics that do not require a live pane,
// including the terminated-pane verdict CLIStatusCandidates records for an
// absent error row (process-exited, unknown-exit).
func (i *Instance) HistoricalSubstate() Substate {
	if held, _ := i.IsAuthHeld(); held {
		return SubstateAuth401
	}
	// The transcript scan is ~1ms a row and an archive backlog has hundreds;
	// the TUI never shows usage-limit on an archived row either.
	if !i.IsArchived() && i.usageLimited() {
		return SubstateUsageLimit
	}
	return i.getTerminatedPaneSubstate()
}
