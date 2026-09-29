package daemon

import "time"

// agentCLIHoldLimit caps how long an "update now" may keep one provider from
// taking new tasks. Past it the provider takes work again and the upgrade
// falls back to waiting for a natural idle moment. A var so tests can shrink it.
var agentCLIHoldLimit = 30 * time.Minute

// agentCLIGate is the per-provider claim barrier behind agent CLI upgrades.
// Every field is guarded by Daemon.claimMu, the same lock as pauseClaims and
// claimsInFlight, so "no task of this provider and none being claimed" is
// read in one step with no window between claim and dispatch.
//
// Profile runtimes count under their provider too: a profile's command_name
// is resolved through PATH or is a wrapper around the stock CLI, so it may
// run the very binary being replaced.
type agentCLIGate struct {
	// tasks counts dispatched tasks per provider, from dispatch until
	// handleTask returns (the per-provider twin of activeTasks).
	tasks map[string]int
	// claims counts in-flight claims whose runtime set includes a provider.
	claims map[string]int
	// held lists providers whose runtimes are left out of new claims.
	held map[string]agentCLIHold
	// waiting marks providers with an upgrade waiting on their tasks, so the
	// last one finishing wakes the updater.
	waiting map[string]bool
	// upgrading counts upgrades running now; the daemon's own self-update
	// treats it as busy.
	upgrading int
}

type agentCLIHold struct {
	since     time.Time
	upgrading bool
}

// agentCLIClaim is what one batch claim covers: the runtimes it may ask for
// and the providers those belong to.
type agentCLIClaim struct {
	runtimeIDs []string
	providers  []string
}

// tryEnterClaimFor is tryEnterClaim that also drops runtimes whose provider
// is held for a CLI upgrade. The returned claim must go back through
// exitClaimFor whenever ok is true.
func (d *Daemon) tryEnterClaimFor(runtimeIDs []string) (agentCLIClaim, bool) {
	providerOf := d.runtimeProviders(runtimeIDs)
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.pauseClaims {
		return agentCLIClaim{}, false
	}
	var claim agentCLIClaim
	seen := map[string]bool{}
	for _, id := range runtimeIDs {
		provider := providerOf[id]
		if _, held := d.cliGate.held[provider]; held && provider != "" {
			continue
		}
		claim.runtimeIDs = append(claim.runtimeIDs, id)
		if provider != "" && !seen[provider] {
			seen[provider] = true
			claim.providers = append(claim.providers, provider)
		}
	}
	d.claimsInFlight++
	if d.cliGate.claims == nil {
		d.cliGate.claims = map[string]int{}
	}
	for _, p := range claim.providers {
		d.cliGate.claims[p]++
	}
	return claim, true
}

func (d *Daemon) exitClaimFor(claim agentCLIClaim) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.claimsInFlight--
	for _, p := range claim.providers {
		if d.cliGate.claims[p]--; d.cliGate.claims[p] <= 0 {
			delete(d.cliGate.claims, p)
		}
	}
}

func (d *Daemon) runtimeProviders(runtimeIDs []string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]string, len(runtimeIDs))
	for _, id := range runtimeIDs {
		if rt, ok := d.runtimeIndex[id]; ok {
			out[id] = rt.Provider
		}
	}
	return out
}

// beginProviderTask counts a claimed task against its provider. The poller
// calls it before exitClaimFor, like activeTasks.Add, so the provider is
// never seen idle between claim and dispatch.
func (d *Daemon) beginProviderTask(runtimeID string) string {
	provider := d.runtimeProviders([]string{runtimeID})[runtimeID]
	if provider == "" {
		return ""
	}
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.cliGate.tasks == nil {
		d.cliGate.tasks = map[string]int{}
	}
	d.cliGate.tasks[provider]++
	return provider
}

// finishActiveTask drops one in-flight task and, when that was the last
// task of a provider whose upgrade is waiting, wakes the updater.
func (d *Daemon) finishActiveTask(provider string) {
	kick := false
	if provider != "" {
		d.claimMu.Lock()
		if d.cliGate.tasks[provider]--; d.cliGate.tasks[provider] <= 0 {
			delete(d.cliGate.tasks, provider)
		}
		_, held := d.cliGate.held[provider]
		kick = d.cliGate.tasks[provider] == 0 && (held || d.cliGate.waiting[provider])
		d.claimMu.Unlock()
	}
	d.activeTasks.Add(-1)
	if kick {
		d.kickAgentCLIUpdate()
	}
}

// holdAgentCLIClaims stops new claims for provider (an "update now" click)
// and returns when the hold started. A hold already in place keeps its start.
func (d *Daemon) holdAgentCLIClaims(provider string, now time.Time) time.Time {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.cliGate.held == nil {
		d.cliGate.held = map[string]agentCLIHold{}
	}
	if h, ok := d.cliGate.held[provider]; ok {
		return h.since
	}
	d.cliGate.held[provider] = agentCLIHold{since: now}
	return now
}

// releaseAgentCLIHold lets provider take tasks again unless its upgrade is
// running; that one is released by endAgentCLIUpgrade.
func (d *Daemon) releaseAgentCLIHold(provider string) {
	d.claimMu.Lock()
	h, ok := d.cliGate.held[provider]
	if ok && !h.upgrading {
		delete(d.cliGate.held, provider)
	}
	d.claimMu.Unlock()
	if ok && !h.upgrading {
		d.wakeClaimGate()
	}
}

// tryBeginAgentCLIUpgrade takes the provider for an upgrade when none of its
// tasks is running or being claimed, and holds its claims until
// endAgentCLIUpgrade. Otherwise it reports how many tasks are in the way
// (0 with ok=false means the daemon's own update holds the machine).
func (d *Daemon) tryBeginAgentCLIUpgrade(provider string, now time.Time) (ok bool, running int) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.cliGate.waiting == nil {
		d.cliGate.waiting = map[string]bool{}
	}
	running = d.cliGate.tasks[provider]
	if d.pauseClaims || d.updating.Load() || running > 0 || d.cliGate.claims[provider] > 0 {
		d.cliGate.waiting[provider] = true
		return false, running
	}
	delete(d.cliGate.waiting, provider)
	if d.cliGate.held == nil {
		d.cliGate.held = map[string]agentCLIHold{}
	}
	h, held := d.cliGate.held[provider]
	if !held {
		h.since = now
	}
	h.upgrading = true
	d.cliGate.held[provider] = h
	d.cliGate.upgrading++
	return true, 0
}

// endAgentCLIUpgrade releases what tryBeginAgentCLIUpgrade took, whether
// the upgrade worked or not, and lets queued tasks be claimed right away.
func (d *Daemon) endAgentCLIUpgrade(provider string) {
	d.claimMu.Lock()
	delete(d.cliGate.held, provider)
	d.cliGate.upgrading--
	d.claimMu.Unlock()
	d.wakeClaimGate()
}

// clearAgentCLIWaiting forgets a waiting upgrade that no longer wants to run.
func (d *Daemon) clearAgentCLIWaiting(provider string) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	delete(d.cliGate.waiting, provider)
}

// agentCLIHoldOverdue reports whether some hold has outlived
// agentCLIHoldLimit, so the loop can give it back without waiting a tick.
func (d *Daemon) agentCLIHoldOverdue(now time.Time) bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	for _, h := range d.cliGate.held {
		if !h.upgrading && now.Sub(h.since) >= agentCLIHoldLimit {
			return true
		}
	}
	return false
}

func (d *Daemon) wakeClaimGate() {
	if d.claimGateWakeup == nil {
		return
	}
	select {
	case d.claimGateWakeup <- struct{}{}:
	default:
	}
}
