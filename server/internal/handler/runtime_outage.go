package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const runtimeAlertGrace = 3 * time.Minute

// SweepRuntimeAlerts persists transitions and both delivery queues atomically.
// It is independent of task routing, and runs even when no newly stale rows exist.
func (h *Handler) SweepRuntimeAlerts(ctx context.Context, now time.Time) error {
	starter, ok := h.DB.(txStarter)
	if !ok {
		return errors.New("runtime alert database unavailable")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(1843,1)`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	q := h.Queries.WithTx(tx)
	machines, err := q.ListRuntimeAlertMachines(ctx)
	if err != nil {
		return err
	}
	items := []db.InboxItem{}
	stamp := pgtype.Timestamptz{Time: now, Valid: true}
	for _, m := range machines {
		key := m.MachineKey
		watch, getErr := q.GetRuntimeMachineWatch(ctx, db.GetRuntimeMachineWatchParams{WorkspaceID: m.WorkspaceID, OwnerID: m.OwnerID, MachineKey: key})
		if getErr != nil && !errors.Is(getErr, pgx.ErrNoRows) {
			return getErr
		}
		if errors.Is(getErr, pgx.ErrNoRows) {
			// Establish a baseline; installing this feature must not alert for old,
			// intentionally offline registrations or never-connected profiles.
			if err = q.SetRuntimeMachineWatch(ctx, db.SetRuntimeMachineWatchParams{WorkspaceID: m.WorkspaceID, OwnerID: m.OwnerID, MachineKey: key, Online: true, ObservedOnlineIds: m.OnlineIds}); err != nil {
				return err
			}
			continue
		}
		// Only runtimes observed online by this monitor can begin an outage.
		seen := map[pgtype.UUID]bool{}
		for _, id := range watch.ObservedOnlineIds {
			seen[id] = true
		}
		affected := []pgtype.UUID{}
		for _, id := range m.OfflineIds {
			if seen[id] {
				affected = append(affected, id)
			}
		}
		for _, id := range m.OnlineIds {
			if !seen[id] {
				watch.ObservedOnlineIds = append(watch.ObservedOnlineIds, id)
				seen[id] = true
			}
		}
		online := len(affected) == 0
		if watch.Online && !online {
			if _, err = q.StartRuntimeOutage(ctx, db.StartRuntimeOutageParams{ID: dbid.NewV7(), WorkspaceID: m.WorkspaceID, OwnerID: m.OwnerID, MachineKey: key, MachineName: m.MachineName, RuntimeIds: affected, StartedAt: stamp}); err != nil {
				return err
			}
		}
		if err = q.SetRuntimeMachineWatch(ctx, db.SetRuntimeMachineWatchParams{WorkspaceID: m.WorkspaceID, OwnerID: m.OwnerID, MachineKey: key, Online: online, ObservedOnlineIds: watch.ObservedOnlineIds}); err != nil {
			return err
		}

		outage, err := q.GetActiveRuntimeOutage(ctx, db.GetActiveRuntimeOutageParams{WorkspaceID: m.WorkspaceID, OwnerID: m.OwnerID, MachineKey: key})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if !online {
			if err = q.UpdateRuntimeOutageRuntimes(ctx, db.UpdateRuntimeOutageRuntimesParams{ID: outage.ID, RuntimeIds: affected}); err != nil {
				return err
			}
			outage, err = q.GetRuntimeOutage(ctx, outage.ID)
			if err != nil {
				return err
			}
		}
		enabled, err := runtimeAlertsEnabled(ctx, q, m.WorkspaceID, m.OwnerID)
		if err != nil {
			return err
		}
		eventType := ""
		if online {
			if err = q.EndRuntimeOutage(ctx, db.EndRuntimeOutageParams{ID: outage.ID, EndedAt: stamp}); err != nil {
				return err
			}
			outage.EndedAt = stamp
			if outage.AlertedAt.Valid && enabled {
				eventType = "runtime.online"
			}
		} else if !outage.AlertedAt.Valid && now.Sub(outage.StartedAt.Time) > runtimeAlertGrace && enabled {
			if err = q.AlertRuntimeOutage(ctx, db.AlertRuntimeOutageParams{ID: outage.ID, AlertedAt: stamp}); err != nil {
				return err
			}
			outage.AlertedAt = stamp
			eventType = "runtime.offline"
		}
		if eventType == "" {
			continue
		}
		eventID := dbid.NewV7()
		detail, err := json.Marshal(map[string]string{"runtime_outage_id": uuidToString(outage.ID), "machine_name": outage.MachineName, "started_at": timestampToString(outage.StartedAt), "ended_at": timestampToString(outage.EndedAt)})
		if err != nil {
			return err
		}
		severity, title, body := "attention", outage.MachineName+" 已掉线", "机器上的运行时持续掉线超过 3 分钟，对应智能体暂时不能运行。"
		if online {
			severity, title, body = "info", outage.MachineName+" 已恢复在线", "机器已重新连接，可以继续运行智能体。"
		}
		item, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{ID: eventID, WorkspaceID: m.WorkspaceID, RecipientType: "member", RecipientID: m.OwnerID, Type: eventType, Severity: severity, Title: title, Body: pgtype.Text{String: body, Valid: true}, ActorType: pgtype.Text{String: "system", Valid: true}, Details: detail})
		if err != nil {
			return err
		}
		if err = q.QueueRuntimeOutageWake(ctx, db.QueueRuntimeOutageWakeParams{WorkspaceID: m.WorkspaceID, OwnerID: m.OwnerID, EventType: eventType, SourceID: eventID, OutageID: outage.ID}); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, item := range items {
		h.publishWithSource(ctx, protocol.EventInboxNew, uuidToString(item.WorkspaceID), "system", "", map[string]any{"item": inboxToResponse(item)})
	}
	return nil
}

func runtimeAlertsEnabled(ctx context.Context, q *db.Queries, ws, owner pgtype.UUID) (bool, error) {
	pref, err := q.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{WorkspaceID: ws, UserID: owner})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var prefs map[string]string
	if err = json.Unmarshal(pref.Preferences, &prefs); err != nil {
		return false, err
	}
	return prefs["runtime_alerts"] != "muted", nil
}

func runtimeOutageResponse(o db.RuntimeOutage) map[string]any {
	ids := make([]string, 0, len(o.RuntimeIds))
	for _, id := range o.RuntimeIds {
		ids = append(ids, uuidToString(id))
	}
	return map[string]any{"id": uuidToString(o.ID), "machine_key": o.MachineKey, "machine_name": o.MachineName, "runtime_ids": ids, "started_at": o.StartedAt.Time, "ended_at": timestampToPtr(o.EndedAt), "alerted_at": timestampToPtr(o.AlertedAt)}
}

// Owner-only even for workspace admins: these are personal machine alerts.
func (h *Handler) ListRuntimeOutages(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ws := ctxWorkspaceID(r.Context())
	rows, err := h.Queries.ListRuntimeOutages(r.Context(), db.ListRuntimeOutagesParams{WorkspaceID: parseUUID(ws), OwnerID: parseUUID(user)})
	if err != nil {
		writeError(w, 500, "failed to load runtime outages; try again")
		return
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, runtimeOutageResponse(row))
	}
	writeJSON(w, 200, result)
}
