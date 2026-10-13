package main

// shutdown.go — the order teardown runs in, as named steps rather than as the
// order statements happen to appear in main(). TestShutdownSequence pins it
// against this type.

// shutdownSequence is every teardown step, in the order it must run. A nil step
// is skipped, which is what lets a deployment without a metrics server use the
// same sequence.
type shutdownSequence struct {
	// Drain the internal maintenance listener while the primary pool is alive.
	DrainMaintenance func()

	// DrainHTTP before anything that serves it. In-flight handlers finish
	// calling Schedule() before the scheduler stops.
	DrainHTTP func()

	// StopRelay halts the realtime relay readers once no handler can publish.
	StopRelay func()

	// CancelWorkers cancels the sweeper context every background loop is
	// bound to.
	CancelWorkers func()

	// StopHeartbeats flushes the final batch of queued heartbeat bumps.
	StopHeartbeats func()

	StopMetricsServer func()
	StopProfiling     func()
}

// run performs the sequence. The order here IS the contract.
func (s shutdownSequence) run() {
	for _, step := range []func(){
		s.DrainMaintenance,
		s.DrainHTTP,
		s.StopRelay,
		s.CancelWorkers,
		s.StopHeartbeats,
		s.StopMetricsServer,
		s.StopProfiling,
	} {
		if step != nil {
			step()
		}
	}
}
