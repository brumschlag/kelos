package sessionupdate

// The idle-drain protocol reuses the Request and Report types of the runtime
// update protocol but carries them on dedicated annotations. It lets the
// controller ask a Session Pod to stop accepting new turns and confirm that no
// turn is in flight before the controller deletes an idle Session, closing the
// window where the runtime has locally accepted a turn but not yet published it
// to the Session status.
const (
	// IdleDrainRequestAnnotation asks one specific Session Pod to drain before
	// the controller deletes the Session for exceeding its idle delete policy.
	IdleDrainRequestAnnotation = "kelos.dev/session-idle-drain-request"
	// IdleDrainReportAnnotation is where the runtime acknowledges an idle-drain
	// request, reporting Draining while a turn is still in flight and Drained
	// once the runtime is idle and no longer accepting turns.
	IdleDrainReportAnnotation = "kelos.dev/session-idle-drain-report"
)
