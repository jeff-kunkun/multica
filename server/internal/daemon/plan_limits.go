package daemon

import (
	"context"
	"os"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const planQuotaProbeInterval = 2 * time.Minute

// recordPlanLimits keeps the newest provider snapshot in daemon memory until a
// heartbeat delivers it. Built-in runtimes for the same provider share one CLI
// account across watched workspaces, so a snapshot observed on one is copied to
// its siblings. Custom-profile runtimes remain isolated because their command
// can authenticate as a different provider account.
func (d *Daemon) recordPlanLimits(runtimeID string, snapshot *protocol.PlanLimitsSnapshot) {
	if snapshot == nil || runtimeID == "" {
		return
	}

	d.mu.Lock()
	source, ok := d.runtimeIndex[runtimeID]
	if !ok {
		d.mu.Unlock()
		return
	}
	targets := []string{runtimeID}
	if source.ProfileID == "" {
		targets = targets[:0]
		for id, runtime := range d.runtimeIndex {
			if runtime.ProfileID == "" && runtime.Provider == source.Provider {
				targets = append(targets, id)
			}
		}
	}
	d.mu.Unlock()

	copySnapshot := clonePlanLimitsSnapshot(snapshot)
	copySnapshot.Provider = source.Provider
	d.planLimitsMu.Lock()
	if d.planLimits == nil {
		d.planLimits = make(map[string]protocol.PlanLimitsSnapshot)
	}
	for _, id := range targets {
		d.planLimits[id] = copySnapshot
	}
	d.planLimitsMu.Unlock()
}

func (d *Daemon) planLimitsForRuntime(runtimeID string) *protocol.PlanLimitsSnapshot {
	d.planLimitsMu.RLock()
	snapshot, ok := d.planLimits[runtimeID]
	d.planLimitsMu.RUnlock()
	if !ok {
		return nil
	}
	cloned := clonePlanLimitsSnapshot(&snapshot)
	return &cloned
}

// recordPlanLimitsForProvider copies a live probe snapshot onto every built-in
// runtime of that provider. Custom-profile runtimes stay untouched.
func (d *Daemon) recordPlanLimitsForProvider(provider string, snapshot *protocol.PlanLimitsSnapshot) {
	if provider == "" || snapshot == nil {
		return
	}
	d.mu.Lock()
	var runtimeID string
	for id, runtime := range d.runtimeIndex {
		if runtime.ProfileID == "" && runtime.Provider == provider {
			runtimeID = id
			break
		}
	}
	d.mu.Unlock()
	if runtimeID == "" {
		return
	}
	d.recordPlanLimits(runtimeID, snapshot)
}

// planLimitsByProvider is the localhost /health overlay. Desktop merges these
// onto runtime rows when the cloud API has no stored snapshot (official
// backends still ignore heartbeat plan_limits).
func (d *Daemon) planLimitsByProvider() map[string]protocol.PlanLimitsSnapshot {
	d.mu.Lock()
	providerRuntime := make(map[string]string)
	for id, runtime := range d.runtimeIndex {
		if runtime.ProfileID != "" {
			continue
		}
		if _, ok := providerRuntime[runtime.Provider]; !ok {
			providerRuntime[runtime.Provider] = id
		}
	}
	d.mu.Unlock()

	out := make(map[string]protocol.PlanLimitsSnapshot)
	for provider, runtimeID := range providerRuntime {
		if snap := d.planLimitsForRuntime(runtimeID); snap != nil {
			out[provider] = *snap
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d *Daemon) maybeRefreshPlanQuota() {
	if !d.planQuotaInflight.CompareAndSwap(false, true) {
		return
	}
	d.planQuotaMu.Lock()
	if !d.lastPlanQuotaProbe.IsZero() && time.Since(d.lastPlanQuotaProbe) < planQuotaProbeInterval {
		d.planQuotaMu.Unlock()
		d.planQuotaInflight.Store(false)
		return
	}
	d.lastPlanQuotaProbe = time.Now()
	d.planQuotaMu.Unlock()

	go func() {
		defer d.planQuotaInflight.Store(false)
		d.refreshPlanQuota()
	}()
}

func (d *Daemon) refreshPlanQuota() {
	d.mu.Lock()
	providers := make(map[string]struct{})
	for _, runtime := range d.runtimeIndex {
		if runtime.ProfileID == "" && (runtime.Provider == "claude" || runtime.Provider == "codex") {
			providers[runtime.Provider] = struct{}{}
		}
	}
	d.mu.Unlock()
	if len(providers) == 0 {
		return
	}

	probe := d.planQuotaProbe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for provider := range providers {
		var (
			snapshot *protocol.PlanLimitsSnapshot
			err      error
		)
		switch provider {
		case "claude":
			snapshot, err = probe.ProbeClaude(ctx)
		case "codex":
			snapshot, err = probe.ProbeCodex(ctx)
		default:
			continue
		}
		if err != nil {
			if d.logger != nil {
				d.logger.Debug("plan quota probe failed", "provider", provider, "error", err)
			}
			continue
		}
		if snapshot == nil {
			continue
		}
		d.recordPlanLimitsForProvider(provider, snapshot)
	}
}

func (d *Daemon) planQuotaProbe() agent.PlanQuotaProbe {
	if d.planQuotaProbeFn != nil {
		return d.planQuotaProbeFn()
	}
	home := d.planQuotaHome
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = dir
		}
	}
	probe := agent.PlanQuotaProbe{Home: home, Client: d.planQuotaClient}
	if d.planQuotaClaudeURL != "" {
		probe.ClaudeUsageURL = d.planQuotaClaudeURL
	}
	if d.planQuotaCodexURL != "" {
		probe.CodexUsageURL = d.planQuotaCodexURL
	}
	return probe
}

func clonePlanLimitsSnapshot(snapshot *protocol.PlanLimitsSnapshot) protocol.PlanLimitsSnapshot {
	cloned := *snapshot
	cloned.Windows = make([]protocol.PlanLimitWindow, len(snapshot.Windows))
	for i, window := range snapshot.Windows {
		cloned.Windows[i] = window
		if window.UsedPercent != nil {
			value := *window.UsedPercent
			cloned.Windows[i].UsedPercent = &value
		}
		if window.WindowMinutes != nil {
			value := *window.WindowMinutes
			cloned.Windows[i].WindowMinutes = &value
		}
		if window.ResetsAt != nil {
			value := *window.ResetsAt
			cloned.Windows[i].ResetsAt = &value
		}
	}
	return cloned
}
