package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The reviewer slot is a fixture of the product, not a field each workspace
// has to think up. Every routed ticket answers the same two questions — who
// does the work, who accepts it — so the slot that holds the second answer is
// provisioned by the server the first time routing runs on a workspace.
//
// It stays a `select` property rather than a new issue column because the two
// non-seat answers ("needs no review", "hand it to a person") are values in
// the same slot, and because the picker, the table column and the filters all
// already exist for properties. Nothing new has to be drawn.
//
// Only the server does this. The HTTP route that creates property definitions
// still rejects agents (property.go): a seat cannot invent workspace fields.
// This is the one definition the deployment owns, and it is created on the
// workspace's behalf, not on any caller's authority.
const (
	reviewerPropertyIcon        = "circle-check"
	reviewerPropertyDescription = "谁来验收这张票。路由只在这一格为空时填，从不覆盖已有的值。"
	// Colors are fixed so the slot looks the same in every workspace.
	reviewerSeatColor     = "#6366f1"
	reviewerHumanColor    = "#f59e0b"
	reviewerNoReviewColor = "#64748b"
)

// ensureReviewerProperty returns the workspace's reviewer slot, creating it
// when it is missing and widening its option list when the roster has grown.
//
// ok=false is returned for the two cases routing must treat as "no slot":
// the definition is archived — archiving it is how a workspace turns the
// reviewer half of routing off — or a definition of that name exists with an
// incompatible type, which is somebody else's field and must not be touched.
func (s routingStore) ensureReviewerProperty(ctx context.Context, wsID pgtype.UUID) (routing.ReviewerProperty, bool, error) {
	props, err := s.h.Queries.ListIssueProperties(ctx, db.ListIssuePropertiesParams{
		WorkspaceID: wsID, IncludeArchived: true,
	})
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	for _, p := range props {
		if !strings.EqualFold(p.Name, ReviewerPropertyName) {
			continue
		}
		if p.ArchivedAt.Valid {
			return routing.ReviewerProperty{}, false, nil
		}
		if p.Type != "select" {
			slog.Warn("reviewer property exists with an incompatible type",
				"workspace_id", util.UUIDToString(wsID), "type", p.Type)
			return routing.ReviewerProperty{}, false, nil
		}
		return s.syncReviewerOptions(ctx, wsID, p.ID, parsePropertyConfig(p.Config))
	}
	return s.createReviewerProperty(ctx, wsID, props)
}

func (s routingStore) createReviewerProperty(ctx context.Context, wsID pgtype.UUID, existing []db.ListIssuePropertiesRow) (routing.ReviewerProperty, bool, error) {
	active := 0
	for _, p := range existing {
		if !p.ArchivedAt.Valid {
			active++
		}
	}
	if active >= maxActivePropertiesPerWorkspace {
		// The workspace has spent its catalog on its own fields. Routing
		// fills the executor slot only and says so in its comment; it does
		// not evict somebody else's property to make room.
		slog.Warn("cannot provision reviewer property: workspace property catalog is full",
			"workspace_id", util.UUIDToString(wsID), "active", active)
		return routing.ReviewerProperty{}, false, nil
	}
	seats, err := s.reviewerSeatNames(ctx, wsID)
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	config, err := json.Marshal(PropertyConfig{Options: reviewerOptions(seats, nil)})
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	created, err := s.h.Queries.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
		WorkspaceID: wsID,
		Name:        ReviewerPropertyName,
		Type:        "select",
		Description: reviewerPropertyDescription,
		Icon:        reviewerPropertyIcon,
		Config:      config,
	})
	if err != nil {
		// Two Route calls on the same new issue can reach this line at once;
		// the unique index on (workspace_id, lower(name)) lets exactly one
		// through. The loser reads the winner's row rather than failing the
		// whole routing call.
		if isUniqueViolation(err) {
			return s.findReviewerProperty(ctx, wsID)
		}
		return routing.ReviewerProperty{}, false, err
	}
	slog.Info("provisioned reviewer property",
		"workspace_id", util.UUIDToString(wsID), "property_id", util.UUIDToString(created.ID))
	return reviewerPropertyView(created.ID, parsePropertyConfig(created.Config)), true, nil
}

// syncReviewerOptions adds options for seats hired since the slot was made.
// It only ever appends: removing an option would orphan the value on every
// ticket already holding it, and a seat that left is still the truthful
// answer to "who accepted this".
func (s routingStore) syncReviewerOptions(ctx context.Context, wsID, propID pgtype.UUID, cfg PropertyConfig) (routing.ReviewerProperty, bool, error) {
	seats, err := s.reviewerSeatNames(ctx, wsID)
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	merged := reviewerOptions(seats, cfg.Options)
	if len(merged) == len(cfg.Options) {
		return reviewerPropertyView(propID, cfg), true, nil
	}
	config, err := json.Marshal(PropertyConfig{Options: merged})
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	updated, err := s.h.Queries.UpdateIssueProperty(ctx, db.UpdateIssuePropertyParams{
		ID: propID, WorkspaceID: wsID, Config: config,
	})
	if err != nil {
		// A widened option list is a convenience, not a precondition: the
		// slot as it stands is still writable for every seat already in it.
		slog.Warn("could not widen reviewer property options",
			"workspace_id", util.UUIDToString(wsID), "error", err)
		return reviewerPropertyView(propID, cfg), true, nil
	}
	return reviewerPropertyView(propID, parsePropertyConfig(updated.Config)), true, nil
}

// reviewerOptions merges the desired option names into the existing list,
// preserving existing option ids (issue rows reference them) and order.
func reviewerOptions(seats []string, existing []PropertyOption) []PropertyOption {
	out := append([]PropertyOption(nil), existing...)
	have := make(map[string]struct{}, len(out))
	for _, o := range out {
		have[strings.ToLower(o.Name)] = struct{}{}
	}
	add := func(name, color string) {
		if name == "" {
			return
		}
		if _, dup := have[strings.ToLower(name)]; dup {
			return
		}
		if len(out) >= maxPropertySelectOptions {
			return
		}
		have[strings.ToLower(name)] = struct{}{}
		out = append(out, PropertyOption{ID: uuid.NewString(), Name: name, Color: color})
	}
	// The two non-seat answers go in first. They are what makes the slot
	// closable: "needs no review" has to be a written value, because an
	// empty slot is re-judged on every later status change.
	add(routing.OptionNoReview, reviewerNoReviewColor)
	add(routing.OptionHuman, reviewerHumanColor)
	for _, name := range seats {
		add(name, reviewerSeatColor)
	}
	return out
}

// reviewerSeatNames lists the seats that may appear in the slot. Option names
// share the label-name rules, so a seat whose name the property system would
// reject is left out rather than failing the whole provision.
func (s routingStore) reviewerSeatNames(ctx context.Context, wsID pgtype.UUID) ([]string, error) {
	agents, err := s.h.Queries.ListAgents(ctx, wsID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.ArchivedAt.Valid {
			continue
		}
		name, err := validateLabelName(a.Name)
		if err != nil {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func reviewerPropertyView(id pgtype.UUID, cfg PropertyConfig) routing.ReviewerProperty {
	options := make(map[string]string, len(cfg.Options))
	for _, o := range cfg.Options {
		options[o.Name] = o.ID
	}
	return routing.ReviewerProperty{ID: util.UUIDToString(id), Options: options}
}
