package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/service"
)

var transferCmd = &cobra.Command{
	Use:   "transfer",
	Short: "Export or import a workspace transfer bundle",
}

var transferExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export workspace config and conversations into a zip bundle",
	Args:  cobra.NoArgs,
	RunE:  runTransferExport,
}

var transferImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import a transfer bundle into a kun workspace",
	Args:  cobra.NoArgs,
	RunE:  runTransferImport,
}

// transferBindRuntimesCmd applies the runtime bindings the migration card
// collected for the agents the auto-bind rule deliberately left alone
// (DENE-364). It is the same write the agent editor performs, so it stays a
// separate, explicit action rather than a second import.
var transferBindRuntimesCmd = &cobra.Command{
	Use:   "bind-runtimes",
	Short: "Bind imported agents to runtimes on the target instance",
	Args:  cobra.NoArgs,
	RunE:  runTransferBindRuntimes,
}

func init() {
	transferExportCmd.Flags().String("workspace", "", "Source workspace slug or id")
	transferExportCmd.Flags().String("out", "", "Output zip path")
	transferExportCmd.Flags().String("include", "config,conversations,attachments", "Comma-separated parts to include: config,conversations,attachments,issues")
	transferExportCmd.Flags().Bool("estimate", false, "Estimate size without writing a bundle")
	transferExportCmd.Flags().Bool("exclude-archived", false, "Skip archived chats")
	transferExportCmd.Flags().Bool("no-people", false, "Omit people.json (member refs other than exporter will degrade)")
	_ = transferExportCmd.MarkFlagRequired("workspace")

	transferImportCmd.Flags().String("workspace", "", "Target workspace slug or id")
	transferImportCmd.Flags().String("in", "", "Input zip or V1 JSON path")
	transferImportCmd.Flags().Bool("dry-run", false, "Preview without writing")
	transferImportCmd.Flags().String("on-conflict", "fail", "Conflict policy for config entities: fail, overwrite, rename, skip")
	transferImportCmd.Flags().Bool("renumber", false, "Offset every imported issue number by the target workspace's watermark (needs a non-empty target and asks for confirmation)")
	registerTransferImportOptionFlags(transferImportCmd)
	_ = transferImportCmd.MarkFlagRequired("workspace")
	_ = transferImportCmd.MarkFlagRequired("in")

	transferBindRuntimesCmd.Flags().String("workspace", "", "Target workspace slug or id")
	transferBindRuntimesCmd.Flags().StringArray("bind", nil, "Agent to runtime binding as <agent_id>=<runtime_id>; repeat per agent")
	_ = transferBindRuntimesCmd.MarkFlagRequired("workspace")
	_ = transferBindRuntimesCmd.MarkFlagRequired("bind")

	transferCmd.AddCommand(transferExportCmd)
	transferCmd.AddCommand(transferImportCmd)
	transferCmd.AddCommand(transferBindRuntimesCmd)
}

// registerTransferImportOptionFlags declares the config-import switches
// `transfer import` forwards to the target. The defaults mirror the Desktop
// migration card: moving a workspace across environments is meant to reproduce
// the same environment, so autopilots come across running. The issue prefix is
// the one switch whose default depends on the bundle — see
// transferImportOptionsForBundle.
func registerTransferImportOptionFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("activate-autopilots", true, "Keep the source autopilot status; imported automations start triggering immediately (--activate-autopilots=false imports them paused)")
	cmd.Flags().Bool("apply-workspace-settings", true, "Apply the exported workspace settings (context, repos, attribution)")
	cmd.Flags().Bool("apply-issue-prefix", false, "Adopt the exported issue prefix. On by default for a bundle that carries the issues group, whose target must be empty: that is what keeps every `<PREFIX>-xxx` reference in the imported bodies and comments pointing at its own issue. Pass --apply-issue-prefix=false to opt out; adopting it changes the key of every future issue in the target")
	cmd.Flags().Bool("auto-bind-runtimes", true, "Bind each imported agent to the target runtime matching its source provider, mode and profile when exactly one exists (--auto-bind-runtimes=false leaves every agent for manual binding)")
}

// transferImportOptions reads the switches registered above. A command built
// without them falls back to the same defaults.
func transferImportOptions(cmd *cobra.Command) service.ConfigImportOptions {
	activate, _ := cmd.Flags().GetBool("activate-autopilots")
	applySettings, _ := cmd.Flags().GetBool("apply-workspace-settings")
	applyPrefix, _ := cmd.Flags().GetBool("apply-issue-prefix")
	autoBind := true
	if f := cmd.Flags().Lookup("auto-bind-runtimes"); f != nil {
		autoBind, _ = cmd.Flags().GetBool("auto-bind-runtimes")
	}
	return service.ConfigImportOptions{
		ActivateAutopilots:     activate,
		ApplyWorkspaceSettings: &applySettings,
		ApplyIssuePrefix:       applyPrefix,
		AutoBindRuntimes:       &autoBind,
	}
}

// transferImportOptionsForBundle resolves the switches for a loaded bundle.
//
// The issue prefix follows the task group (DENE-404). "Adopting the prefix
// changes every later issue key, so it stays off" only holds for a non-empty
// target: the V3 contract makes an empty target a hard precondition for the
// issues group, and an empty target is exactly where every `<PREFIX>-xxx`
// reference in the imported bodies and comments keeps pointing at its own issue
// only because the prefix lands with it. Leaving the switch off there keeps the
// numbers but renames the workspace, so the two identifier lists differ and
// nothing on screen says why. The empty-target guard in the import kernel still
// protects a target that already holds tasks: it skips the write and reports
// `issue_prefix_skipped_target_has_issues`.
//
// An explicit flag always wins, in both directions, so a user who wants the
// numbers without the prefix can still say so.
func transferImportOptionsForBundle(cmd *cobra.Command, hasIssues bool) service.ConfigImportOptions {
	opts := transferImportOptions(cmd)
	if f := cmd.Flags().Lookup("apply-issue-prefix"); hasIssues && f != nil && !f.Changed {
		opts.ApplyIssuePrefix = true
	}
	return opts
}

type sourceClient struct {
	api *cli.APIClient
}

func (s sourceClient) GetJSON(ctx context.Context, path string, out any) error {
	var err error
	backoff := time.Second
	for attempt := 0; attempt < 8; attempt++ {
		err = wrapTransferHTTP(s.api.GetJSON(ctx, path, out))
		st := 0
		var te *service.TransferHTTPError
		if errors.As(err, &te) {
			st = te.Status
		}
		if st != 429 && st != 503 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
	return err
}

func (s sourceClient) GetBytes(ctx context.Context, path string) ([]byte, error) {
	data, err := s.api.DownloadFile(ctx, path)
	return data, wrapTransferHTTP(err)
}

func wrapTransferHTTP(err error) error {
	if err == nil {
		return nil
	}
	var he *cli.HTTPError
	if errors.As(err, &he) {
		return &service.TransferHTTPError{Status: he.StatusCode, Err: err}
	}
	return err
}

func newTransferAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	client.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	return client, nil
}

func parseInclude(raw string) []string {
	parts := []string{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func includesTransferGroup(include []string, group string) bool {
	for _, p := range include {
		if p == group {
			return true
		}
	}
	return false
}

// transferProgressEvent is one line of the progress protocol the Desktop
// migration card reads off the CLI's stderr: a JSON object per line, while
// stdout stays reserved for the command's own output. Fields are omitted when
// they carry nothing to say, so the card never renders a "0 / 0" placeholder
// (DENE-318).
type transferProgressEvent struct {
	Event         string `json:"event"`
	SessionIndex  int    `json:"session_index,omitempty"`
	SessionsTotal int    `json:"sessions_total,omitempty"`
	SessionTitle  string `json:"session_title,omitempty"`
	// AttachmentsDownloaded reports the export direction,
	// AttachmentsUploaded the import direction. The Desktop card keeps one
	// attachment counter for both.
	AttachmentsDownloaded int `json:"attachments_downloaded,omitempty"`
	AttachmentsUploaded   int `json:"attachments_uploaded,omitempty"`
	AttachmentsTotal      int `json:"attachments_total,omitempty"`
}

type transferProgressReporter struct {
	enc *json.Encoder
}

func newTransferProgressReporter(w io.Writer) *transferProgressReporter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &transferProgressReporter{enc: enc}
}

func (r *transferProgressReporter) reportExport(p service.TransferExportProgress) {
	r.write(transferProgressEvent{
		Event:                 "progress",
		SessionIndex:          p.SessionIndex,
		SessionsTotal:         p.SessionsTotal,
		SessionTitle:          p.SessionTitle,
		AttachmentsDownloaded: p.AttachmentsDownloaded,
	})
}

func (r *transferProgressReporter) reportUploaded(uploaded, total int) {
	r.write(transferProgressEvent{
		Event:               "progress",
		AttachmentsUploaded: uploaded,
		AttachmentsTotal:    total,
	})
}

// write never fails the transfer: progress is a courtesy, and a closed stderr
// must not abort an export that is otherwise working.
func (r *transferProgressReporter) write(ev transferProgressEvent) {
	if r == nil || r.enc == nil {
		return
	}
	_ = r.enc.Encode(ev)
}

func runTransferExport(cmd *cobra.Command, _ []string) error {
	workspace, _ := cmd.Flags().GetString("workspace")
	outPath, _ := cmd.Flags().GetString("out")
	include, _ := cmd.Flags().GetString("include")
	estimate, _ := cmd.Flags().GetBool("estimate")
	excludeArchived, _ := cmd.Flags().GetBool("exclude-archived")
	noPeople, _ := cmd.Flags().GetBool("no-people")
	if !estimate && outPath == "" {
		return fmt.Errorf("--out is required unless --estimate is set")
	}

	client, err := newTransferAPIClient(cmd)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "This bundle will contain full chat history and member emails. Keep it as a sensitive file; do not upload it to a public location.")
	if includesTransferGroup(parseInclude(include), service.TransferIncludeIssues) {
		// The issues group is not a merge: it relies on the source numbers
		// staying authoritative, which only holds in an empty target (§2.2).
		fmt.Fprintln(cmd.ErrOrStderr(), "The issues group requires the target workspace to have no issues at all.")
	}

	host := ""
	if u, err := url.Parse(client.BaseURL); err == nil {
		host = u.Hostname()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	// --workspace is the authoritative source selection for export. Resolve it
	// before any workspace-scoped request and bind the client to the UUID so
	// profiles without a default workspace_id work (notably Desktop).
	workspaceID, err := resolveTransferWorkspaceID(ctx, sourceClient{api: client}, workspace)
	if err != nil {
		return fmt.Errorf("resolve source workspace: %w", err)
	}
	client.WorkspaceID = workspaceID

	partial := ""
	if !estimate && outPath != "" {
		partial = outPath + ".partial"
		if err := os.MkdirAll(partial, 0o700); err != nil {
			return err
		}
	}

	files, err := service.ExportFromSource(ctx, sourceClient{api: client}, service.TransferExportOpts{
		Include:         parseInclude(include),
		ExcludeArchived: excludeArchived,
		People:          !noPeople,
		Estimate:        estimate,
		ClientVersion:   version,
		BaseURLHost:     host,
		WorkspaceRef:    workspace,
		PartialDir:      partial,
		OutPath:         outPath,
		Progress:        newTransferProgressReporter(cmd.ErrOrStderr()).reportExport,
	})
	if err != nil {
		return err
	}
	if estimate && files.Estimate != nil {
		return cli.PrintJSON(cmd.OutOrStdout(), files.Estimate)
	}
	if n := len(files.People); n > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Bundle contains %d member email(s) used to match people in the target workspace.\n", n)
	}
	if err := writeTransferZip(outPath, files); err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Export complete. Store the zip privately; it contains chat history and member emails.")
	fmt.Fprintln(cmd.OutOrStdout(), outPath)
	return nil
}

func writeTransferZip(outPath string, files *service.TransferExportFiles) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		if filepath.Dir(outPath) != "." {
			return err
		}
	}
	partial := outPath + ".partial"
	// Keep a valid existing checkpoint; only remove it after the zip is
	// atomically renamed into place.
	if err := os.MkdirAll(partial, 0o700); err != nil {
		return err
	}
	entries := map[string][]byte{}
	put := func(path string, data []byte) {
		entries[path] = data
	}
	cfg, err := json.MarshalIndent(files.Config, "", "  ")
	if err != nil {
		return err
	}
	put("config.json", append(cfg, '\n'))
	if len(files.People) > 0 {
		b, _ := json.MarshalIndent(files.People, "", "  ")
		put("people.json", append(b, '\n'))
	}
	rt, _ := json.MarshalIndent(files.Runtimes, "", "  ")
	put("runtime_profiles.json", append(rt, '\n'))
	pr, _ := json.MarshalIndent(files.Preferences, "", "  ")
	put("preferences.json", append(pr, '\n'))
	sec, _ := json.MarshalIndent(files.Secrets, "", "  ")
	put("secrets_omitted.json", append(sec, '\n'))

	for i, shard := range files.SessionShards {
		name := fmt.Sprintf("conversations/sessions-%04d.jsonl", i+1)
		put(name, marshalJSONL(shard))
	}
	for i, shard := range files.MessageShards {
		name := fmt.Sprintf("conversations/messages-%04d.jsonl", i+1)
		put(name, marshalJSONL(shard))
	}
	for i, shard := range files.IssueShards {
		put(fmt.Sprintf("issues/issues-%04d.jsonl", i+1), marshalJSONL(shard))
		if i < len(files.CommentShards) {
			put(fmt.Sprintf("issues/comments-%04d.jsonl", i+1), marshalJSONL(files.CommentShards[i]))
		}
	}
	if len(files.Relations) > 0 {
		put("issues/relations.jsonl", marshalJSONL(files.Relations))
	}
	if len(files.Attachments) > 0 {
		put("attachments/index.jsonl", marshalJSONL(files.Attachments))
	}
	for sha, blob := range files.Blobs {
		put("attachments/blobs/"+sha, blob)
	}

	var fileMeta []service.TransferFileMeta
	for path, data := range entries {
		sum := sha256.Sum256(data)
		meta := service.TransferFileMeta{Path: path, SHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}
		if strings.HasSuffix(path, ".jsonl") {
			meta.Rows = bytes.Count(data, []byte("\n"))
			if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
				meta.Rows++
			}
		}
		fileMeta = append(fileMeta, meta)
	}
	files.Manifest.Files = fileMeta
	man, err := json.MarshalIndent(files.Manifest, "", "  ")
	if err != nil {
		return err
	}
	put("manifest.json", append(man, '\n'))

	tmpZip := filepath.Join(partial, "bundle.zip")
	f, err := os.OpenFile(tmpZip, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for path, data := range entries {
		w, err := zw.Create(path)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(data); err != nil {
			f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpZip, outPath); err != nil {
		return err
	}
	_ = os.Chmod(outPath, 0o600)
	_ = os.RemoveAll(partial)
	return nil
}

func marshalJSONL[T any](rows []T) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		_ = enc.Encode(row)
	}
	return buf.Bytes()
}

func runTransferImport(cmd *cobra.Command, _ []string) error {
	workspace, _ := cmd.Flags().GetString("workspace")
	inPath, _ := cmd.Flags().GetString("in")
	dry, _ := cmd.Flags().GetBool("dry-run")
	onConflict, _ := cmd.Flags().GetString("on-conflict")
	renumber, _ := cmd.Flags().GetBool("renumber")

	client, err := newTransferAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	wsID, err := resolveTransferWorkspaceID(ctx, sourceClient{api: client}, workspace)
	if err != nil {
		return err
	}

	payload, err := loadTransferInput(inPath)
	if err != nil {
		return err
	}
	// Resolved after the bundle is in hand: whether this import creates tasks
	// decides the issue-prefix default (DENE-404).
	importOptions := transferImportOptionsForBundle(cmd, len(payload.IssueShards) > 0)

	base := "/api/workspaces/" + url.PathEscape(wsID)
	if payload.V1Only {
		var report any
		if err := client.PostJSON(ctx, base+"/transfer/config", service.TransferConfigRequest{
			Config: payload.Config, DryRun: &dry, OnConflict: onConflict, Options: importOptions,
		}, &report); err != nil {
			if st := httpStatusOf(err); st == 404 {
				return fmt.Errorf("target_unsupported: this server is not a kun instance with /transfer/* endpoints")
			}
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), report)
	}

	// --renumber rewrites the numbers BEFORE anything is written, and it asks
	// for confirmation first: the offset silently invalidates every plain-text
	// `<prefix>-xxx` reference in the imported bodies and comments (§2.3/§2.4).
	issueShards := payload.IssueShards
	numberMapPath := ""
	sourcePrefix := payload.Config.Source.IssuePrefix
	if renumber && len(issueShards) > 0 {
		offset, err := fetchTransferIssueCounter(ctx, client, wsID)
		if err != nil {
			return fmt.Errorf("read target issue watermark: %w", err)
		}
		targetPrefix := fetchTransferIssuePrefix(ctx, client, wsID)
		numberMapPath = inPath + ".number-map.csv"
		if err := confirmTransferRenumber(cmd, sourcePrefix, numberMapPath); err != nil {
			return err
		}
		// Land the table before the writes, so the file the confirmation points
		// at exists even when the import fails halfway through. What the target
		// already holds wins over the computed number: the rows are keyed by
		// their deterministic id and `ON CONFLICT DO NOTHING` keeps the number
		// the first run wrote, so a rerun's fresh offset — finalize moved the
		// watermark — would name numbers that exist nowhere.
		landed, err := fetchTransferLandedIssueNumbers(ctx, client, wsID, transferIssueNumberRefs(payload))
		if err != nil {
			return fmt.Errorf("read imported issue numbers: %w", err)
		}
		if err := writeTransferNumberMap(numberMapPath, buildTransferNumberMap(payload, wsID, sourcePrefix, targetPrefix, offset, landed)); err != nil {
			return err
		}
		issueShards = renumberTransferIssues(issueShards, offset)
	}

	var cfgReport any
	cfgReq := service.TransferConfigRequest{
		Manifest:        payload.Manifest,
		People:          payload.People,
		RuntimeProfiles: payload.Runtimes,
		Preferences:     payload.Preferences,
		Config:          payload.Config,
		SecretsOmitted:  payload.Secrets,
		DryRun:          &dry,
		OnConflict:      onConflict,
		Options:         importOptions,
	}
	if err := client.PostJSON(ctx, base+"/transfer/config", cfgReq, &cfgReport); err != nil {
		if st := httpStatusOf(err); st == 404 {
			return fmt.Errorf("target_unsupported: this server is not a kun instance with /transfer/* endpoints")
		}
		return err
	}

	// Issues go after config (labels, projects, agents and the status catalog
	// must exist) and before attachments (attachment.comment_id references
	// comment.id).
	issuesFold := &transferIssuesReportFold{}
	if len(payload.IssueShards) > 0 {
		if err := uploadTransferIssueShards(ctx, client, base, payload, issueShards, dry, renumber, issuesFold); err != nil {
			return err
		}
	}

	for i := range payload.SessionShards {
		req := service.TransferConversationsRequest{
			Refs:     payload.Manifest.Refs,
			Sessions: payload.SessionShards[i],
			DryRun:   &dry,
		}
		if i < len(payload.MessageShards) {
			req.Messages = payload.MessageShards[i]
		}
		req.Finalize = i == len(payload.SessionShards)-1 && len(payload.Attachments) == 0
		var convReport any
		if err := client.PostJSON(ctx, base+"/transfer/conversations", req, &convReport); err != nil {
			return fmt.Errorf("upload conversations shard %d: %w", i+1, err)
		}
	}
	if !dry {
		// Attachment uploads are the long silent stretch of an import (a real
		// workspace carries minutes' worth of them), so report each one.
		progress := newTransferProgressReporter(cmd.ErrOrStderr())
		progress.reportUploaded(0, len(payload.Attachments))
		for i, att := range payload.Attachments {
			var blob []byte
			if att.SHA256 != "" {
				blob = payload.Blobs[att.SHA256]
			}
			reason := ""
			if att.BodyOmittedReason != nil {
				reason = *att.BodyOmittedReason
			}
			_ = reason
			if err := postTransferAttachment(ctx, client, base+"/transfer/attachments", att, blob); err != nil {
				return fmt.Errorf("upload attachment %s: %w", att.SourceID, err)
			}
			progress.reportUploaded(i+1, len(payload.Attachments))
		}
		if len(payload.SessionShards) > 0 {
			fin := service.TransferConversationsRequest{DryRun: boolPtr(false), Finalize: true, Refs: payload.Manifest.Refs}
			var convReport any
			if err := client.PostJSON(ctx, base+"/transfer/conversations", fin, &convReport); err != nil {
				return fmt.Errorf("finalize conversations: %w", err)
			}
		}
		if len(payload.IssueShards) > 0 {
			if err := finalizeTransferIssues(ctx, client, base, payload, renumber, issuesFold); err != nil {
				return err
			}
		}
	}
	if numberMapPath != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Number mapping table written to %s\n", numberMapPath)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), transferImportOutput(cfgReport, issuesFold))
}

// transferIssuesReportFold folds the per-request `TransferIssuesReport` values
// one import produces into the single report the CLI prints.
//
// Importing tasks is not one request: every shard answers with its own slice of
// the truth and the finalize pass answers with the watermark, so the counts are
// summed, the per-row degradation lists concatenated, the `mention_unmapped`
// summary accumulated per kind, and the watermark / quota policy taken from the
// pass that resolved them. Dropping this fold is what left the Desktop
// migration card's task section — and every degradation the contract promises
// to echo — empty on a real import.
type transferIssuesReportFold struct {
	seen   bool
	report service.TransferIssuesReport
}

func (f *transferIssuesReportFold) add(shard service.TransferIssuesReport) {
	f.seen = true
	f.report.Applied = f.report.Applied || shard.Applied
	f.report.IssuesCreated += shard.IssuesCreated
	f.report.IssuesSkipped += shard.IssuesSkipped
	f.report.CommentsCreated += shard.CommentsCreated
	f.report.CommentsSkipped += shard.CommentsSkipped
	f.report.LabelsCreated += shard.LabelsCreated
	f.report.LabelsSkipped += shard.LabelsSkipped
	f.report.ReactionsCreated += shard.ReactionsCreated
	f.report.ReactionsSkipped += shard.ReactionsSkipped
	f.report.SubscribersCreated += shard.SubscribersCreated
	f.report.SubscribersSkipped += shard.SubscribersSkipped
	f.report.ParentsBackfilled += shard.ParentsBackfilled
	f.report.Finalized = f.report.Finalized || shard.Finalized
	// The watermark only moves at finalize and the quota policy is resolved
	// before the first write, so a shard that did not resolve them reports
	// their zero value; keeping the last non-empty one is what lets a shard
	// report and a finalize report share one shape.
	if shard.IssueCounter != 0 {
		f.report.IssueCounter = shard.IssueCounter
	}
	if shard.IssueLimit != nil {
		f.report.IssueLimit = shard.IssueLimit
	}
	f.report.StatusUnmapped = append(f.report.StatusUnmapped, shard.StatusUnmapped...)
	f.report.AssigneeUnmapped = append(f.report.AssigneeUnmapped, shard.AssigneeUnmapped...)
	f.report.CreatorUnmapped = append(f.report.CreatorUnmapped, shard.CreatorUnmapped...)
	f.report.ProjectUnmapped = append(f.report.ProjectUnmapped, shard.ProjectUnmapped...)
	f.report.AuthorUnmapped = append(f.report.AuthorUnmapped, shard.AuthorUnmapped...)
	f.report.ResolutionUnmapped = append(f.report.ResolutionUnmapped, shard.ResolutionUnmapped...)
	f.report.PropertyUnmapped = append(f.report.PropertyUnmapped, shard.PropertyUnmapped...)
	f.report.ParentUnmapped = append(f.report.ParentUnmapped, shard.ParentUnmapped...)
	f.report.ParentReparentedToAncestor = append(f.report.ParentReparentedToAncestor, shard.ParentReparentedToAncestor...)
	f.report.MentionUnmapped = append(f.report.MentionUnmapped, shard.MentionUnmapped...)
	f.report.LabelUnmapped = append(f.report.LabelUnmapped, shard.LabelUnmapped...)
	f.report.ReactionUnmapped = append(f.report.ReactionUnmapped, shard.ReactionUnmapped...)
	for kind, count := range shard.MentionUnmappedByType {
		if f.report.MentionUnmappedByType == nil {
			f.report.MentionUnmappedByType = map[string]int{}
		}
		f.report.MentionUnmappedByType[kind] += count
	}
}

// decodeTransferIssuesReport reads one `/transfer/issues` answer.
//
// It decodes into the report struct rather than `any` so the seam the Desktop
// card parses is pinned on this side too; a body that is not the report object
// leaves the fold empty instead of failing an import the target has already
// committed.
func decodeTransferIssuesReport(raw json.RawMessage) service.TransferIssuesReport {
	var report service.TransferIssuesReport
	_ = json.Unmarshal(raw, &report)
	return report
}

// transferImportOutput merges the folded task report into the config report the
// CLI forwards on stdout.
//
// The Desktop main process reads the task half from the root-level
// `issues_report` key, so it is a sibling of `config_report` rather than a
// field inside it. A bundle without an `issues` group has no task half and the
// config report is printed untouched.
func transferImportOutput(cfgReport any, fold *transferIssuesReportFold) any {
	if !fold.seen {
		return cfgReport
	}
	out, ok := cfgReport.(map[string]any)
	if !ok {
		out = map[string]any{}
	}
	out["issues_report"] = fold.report
	return out
}

// uploadTransferIssueShards sends every issue shard.
//
// `refs` is the WHOLE package's refs on every request, not the shard's own
// slice: the target's "no foreign issues" gate recognizes the rows this bundle
// already wrote by looking their deterministic ids up in refs.issues, so a
// shard that carried only its own ids would make shard 2 read shard 1's rows as
// issues that were already there and answer 400.
//
// `renumber` travels with every shard for the same reason the gate runs on
// every shard: the flag is what lets a non-empty target accept the write at all
// (contract §2.3), so a shard that dropped it would 400 halfway through.
func uploadTransferIssueShards(ctx context.Context, client *cli.APIClient, base string, payload *loadedTransfer, shards [][]service.TransferIssueRow, dry, renumber bool, fold *transferIssuesReportFold) error {
	// Relations travel in the shard that holds the rows they point at, because
	// both reaction tables carry a real foreign key to their comment/issue.
	commentIssue := map[string]string{}
	for _, shard := range payload.CommentShards {
		for _, c := range shard {
			commentIssue[c.SourceID] = c.IssueID
		}
	}
	shardOfIssue := map[string]int{}
	for i, shard := range payload.IssueShards {
		for _, row := range shard {
			shardOfIssue[row.SourceID] = i
		}
	}
	relationsByShard := make([][]service.TransferRelationRow, len(shards))
	for _, rel := range payload.Relations {
		key := rel.IssueID
		if rel.CommentID != "" {
			key = commentIssue[rel.CommentID]
		}
		idx, ok := shardOfIssue[key]
		if !ok || idx >= len(relationsByShard) {
			continue
		}
		relationsByShard[idx] = append(relationsByShard[idx], rel)
	}

	for i := range shards {
		req := service.TransferIssuesRequest{
			Refs:      payload.Manifest.Refs,
			Issues:    shards[i],
			Relations: relationsByShard[i],
			DryRun:    &dry,
			Renumber:  renumber,
		}
		if i < len(payload.CommentShards) {
			req.Comments = payload.CommentShards[i]
		}
		var raw json.RawMessage
		if err := client.PostJSON(ctx, base+"/transfer/issues", req, &raw); err != nil {
			if st := httpStatusOf(err); st == 404 {
				return fmt.Errorf("target_unsupported: this server is not a kun instance with /transfer/issues")
			}
			return fmt.Errorf("upload issues shard %d: %w", i+1, err)
		}
		fold.add(decodeTransferIssuesReport(raw))
	}
	return nil
}

// finalizeTransferIssues sends the second pass: the whole package's link rows,
// with no bodies.
//
// Both parent pointers are backfilled inside this one request, so it must carry
// the (source_id, parent_issue_id) pairs AND the (source_id, issue_id,
// parent_id) pairs. A finalize that sends an empty `comments` array leaves
// every reply's parent NULL and silently flattens every thread while the report
// still reads clean; sending link rows instead of whole comments is what keeps
// a few thousand rows inside the request-body cap. `renumber` must ride along:
// the empty-target gate runs on this request too.
func finalizeTransferIssues(ctx context.Context, client *cli.APIClient, base string, payload *loadedTransfer, renumber bool, fold *transferIssuesReportFold) error {
	seenIssue := map[string]bool{}
	var issues []service.TransferIssueRow
	for _, shard := range payload.IssueShards {
		for _, row := range shard {
			if seenIssue[row.SourceID] {
				continue
			}
			seenIssue[row.SourceID] = true
			issues = append(issues, service.TransferIssueRow{SourceID: row.SourceID, ParentIssueID: row.ParentIssueID})
		}
	}
	seenComment := map[string]bool{}
	var comments []service.TransferCommentRow
	for _, shard := range payload.CommentShards {
		for _, row := range shard {
			if seenComment[row.SourceID] {
				continue
			}
			seenComment[row.SourceID] = true
			comments = append(comments, service.TransferCommentRow{
				SourceID: row.SourceID,
				IssueID:  row.IssueID,
				ParentID: row.ParentID,
			})
		}
	}
	req := service.TransferIssuesRequest{
		Refs:     payload.Manifest.Refs,
		Issues:   issues,
		Comments: comments,
		DryRun:   boolPtr(false),
		Finalize: true,
		Renumber: renumber,
	}
	if n := transferRequestBytes(req); n > service.TransferConversationsMaxBytes {
		return fmt.Errorf("issues finalize payload is %d bytes, over the %d byte request cap; parent pointers cannot be trimmed without flattening threads",
			n, service.TransferConversationsMaxBytes)
	}
	var raw json.RawMessage
	if err := client.PostJSON(ctx, base+"/transfer/issues", req, &raw); err != nil {
		return fmt.Errorf("finalize issues: %w", err)
	}
	fold.add(decodeTransferIssuesReport(raw))
	return nil
}

func transferRequestBytes(req service.TransferIssuesRequest) int {
	raw, err := json.Marshal(req)
	if err != nil {
		return 0
	}
	return len(raw)
}

// confirmTransferRenumber is the §2.4 gate: the offset breaks every plain-text
// number reference in the bodies, so the user has to type the word.
func confirmTransferRenumber(cmd *cobra.Command, sourcePrefix, mapPath string) error {
	prefix := sourcePrefix
	if prefix == "" {
		prefix = "<source prefix>"
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"After the offset, every plain-text `%s-xxx` reference in descriptions and comments points at the wrong task. The mapping table is %s.\nType `yes` to continue: ",
		prefix, mapPath)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		return fmt.Errorf("renumber cancelled: expected `yes` on stdin")
	}
	return nil
}

func renumberTransferIssues(shards [][]service.TransferIssueRow, offset int32) [][]service.TransferIssueRow {
	out := make([][]service.TransferIssueRow, len(shards))
	for i, shard := range shards {
		rows := make([]service.TransferIssueRow, len(shard))
		for j, row := range shard {
			row.Number = offset + row.Number
			rows[j] = row
		}
		out[i] = rows
	}
	return out
}

// transferNumberMapRow is one line of `<in>.number-map.csv`. A few thousand
// rows scrolled past on a terminal are worth nothing, which is why this lands
// in a file.
type transferNumberMapRow struct {
	SourceIdentifier string
	TargetIdentifier string
	TargetIssueID    string
}

// transferIssueNumberRef is one bundled issue in shard order, counted once.
type transferIssueNumberRef struct {
	sourceID string
	number   int32
}

func transferIssueNumberRefs(payload *loadedTransfer) []transferIssueNumberRef {
	seen := map[string]bool{}
	var rows []transferIssueNumberRef
	for _, shard := range payload.IssueShards {
		for _, row := range shard {
			if seen[row.SourceID] {
				continue
			}
			seen[row.SourceID] = true
			rows = append(rows, transferIssueNumberRef{sourceID: row.SourceID, number: row.Number})
		}
	}
	return rows
}

// transferLandedNumberLookupChunk bounds how many deterministic ids ride in one
// `GET /api/issues?ids=` query. Ids are UUIDs and the whole package travels
// together, so a query string is not where a few thousand of them belong.
const transferLandedNumberLookupChunk = 50

// fetchTransferLandedIssueNumbers resolves the deterministic ids this bundle
// writes against the target, so `--renumber` reports the numbers the target
// actually holds for them.
//
// A first import finds nothing and every row falls back to the offset. A rerun
// finds the rows the first import landed — same deterministic id, and the
// `ON CONFLICT DO NOTHING` write leaves their number alone — which is the only
// source that agrees with the database once the watermark has moved past it.
func fetchTransferLandedIssueNumbers(ctx context.Context, client *cli.APIClient, wsID string, refs []transferIssueNumberRef) (map[string]int32, error) {
	landed := map[string]int32{}
	if len(refs) == 0 {
		return landed, nil
	}
	src := sourceClient{api: client}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, service.TransferIssueID(wsID, ref.sourceID).String())
	}
	for start := 0; start < len(ids); start += transferLandedNumberLookupChunk {
		end := min(start+transferLandedNumberLookupChunk, len(ids))
		q := url.Values{}
		// `/api/issues` is the one request in the import path that is not
		// scoped by its URL, so the target workspace has to ride in the query:
		// the client's own workspace comes from the ambient CLI profile, which
		// on a freshly logged-in target (the migration case, notably Desktop)
		// is empty and 400s, and when it is set at all it names the wrong
		// workspace. `workspace_id` outranks the X-Workspace-ID header.
		q.Set("workspace_id", wsID)
		q.Set("ids", strings.Join(ids[start:end], ","))
		q.Set("limit", strconv.Itoa(transferLandedNumberLookupChunk))
		var envelope struct {
			Issues []struct {
				ID     string `json:"id"`
				Number int32  `json:"number"`
			} `json:"issues"`
		}
		if err := src.GetJSON(ctx, "/api/issues?"+q.Encode(), &envelope); err != nil {
			return nil, err
		}
		for _, issue := range envelope.Issues {
			landed[issue.ID] = issue.Number
		}
	}
	return landed, nil
}

func buildTransferNumberMap(payload *loadedTransfer, wsID, sourcePrefix, targetPrefix string, offset int32, landed map[string]int32) []transferNumberMapRow {
	var rows []transferNumberMapRow
	for _, ref := range transferIssueNumberRefs(payload) {
		sourceIdentifier := payload.Manifest.Refs.Issues[ref.sourceID].Identifier
		if sourceIdentifier == "" {
			sourceIdentifier = transferNumberIdentifier(sourcePrefix, ref.number)
		}
		targetID := service.TransferIssueID(wsID, ref.sourceID)
		targetNumber := offset + ref.number
		if number, ok := landed[targetID.String()]; ok {
			targetNumber = number
		}
		rows = append(rows, transferNumberMapRow{
			SourceIdentifier: sourceIdentifier,
			TargetIdentifier: transferNumberIdentifier(targetPrefix, targetNumber),
			TargetIssueID:    targetID.String(),
		})
	}
	return rows
}

func transferNumberIdentifier(prefix string, number int32) string {
	if prefix == "" {
		return strconv.Itoa(int(number))
	}
	return prefix + "-" + strconv.Itoa(int(number))
}

func writeTransferNumberMap(path string, rows []transferNumberMapRow) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.Write([]string{"source_identifier", "target_identifier", "target_issue_id"}); err != nil {
		return err
	}
	for _, row := range rows {
		if err := w.Write([]string{row.SourceIdentifier, row.TargetIdentifier, row.TargetIssueID}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func fetchTransferIssuePrefix(ctx context.Context, client *cli.APIClient, wsID string) string {
	var ws struct {
		IssuePrefix string `json:"issue_prefix"`
	}
	if err := client.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(wsID), &ws); err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// runTransferBindRuntimes sends the card's picks to the target instance.
// Parsing "-" rather than "=" in an agent UUID is impossible, so "=" is the
// separator; a malformed pair is a usage error, not a silent skip.
func runTransferBindRuntimes(cmd *cobra.Command, _ []string) error {
	workspace, _ := cmd.Flags().GetString("workspace")
	raws, _ := cmd.Flags().GetStringArray("bind")

	bindings := make([]service.TransferRuntimeBinding, 0, len(raws))
	for _, raw := range raws {
		agentID, runtimeID, ok := strings.Cut(strings.TrimSpace(raw), "=")
		agentID = strings.TrimSpace(agentID)
		runtimeID = strings.TrimSpace(runtimeID)
		if !ok || agentID == "" || runtimeID == "" {
			return fmt.Errorf("invalid --bind %q: expected <agent_id>=<runtime_id>", raw)
		}
		bindings = append(bindings, service.TransferRuntimeBinding{AgentID: agentID, RuntimeID: runtimeID})
	}

	client, err := newTransferAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	wsID, err := resolveTransferWorkspaceID(ctx, sourceClient{api: client}, workspace)
	if err != nil {
		return err
	}

	var report service.TransferBindRuntimesReport
	if err := client.PostJSON(ctx, "/api/workspaces/"+url.PathEscape(wsID)+"/transfer/bind-runtimes",
		service.TransferBindRuntimesRequest{Bindings: bindings}, &report); err != nil {
		if st := httpStatusOf(err); st == 404 {
			return fmt.Errorf("target_unsupported: this server is not a kun instance with /transfer/* endpoints")
		}
		return err
	}
	if err := cli.PrintJSON(cmd.OutOrStdout(), report); err != nil {
		return err
	}
	// A partially failed batch still exits 0: the report above carries one line
	// per binding, and exiting non-zero would make the Desktop card throw that
	// detail away and report the whole batch as failed.
	return nil
}

type loadedTransfer struct {
	V1Only        bool
	Manifest      service.TransferManifest
	Config        service.ConfigBundle
	People        []service.TransferPerson
	Runtimes      service.TransferRuntimesFile
	Preferences   service.TransferPreferences
	Secrets       []service.SecretOmitted
	SessionShards [][]service.TransferSessionRow
	MessageShards [][]service.TransferMessageRow
	// V3 issue group. The shard index is the pairing the exporter wrote:
	// CommentShards[i] holds exactly the comments of the issues in IssueShards[i].
	IssueShards   [][]service.TransferIssueRow
	CommentShards [][]service.TransferCommentRow
	Relations     []service.TransferRelationRow
	Attachments   []service.TransferAttachmentRow
	Blobs         map[string][]byte
}

func loadTransferInput(path string) (*loadedTransfer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if looksLikeZip(data) {
		return loadTransferZip(path)
	}
	var bundle service.ConfigBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	if bundle.Format != service.ConfigBundleFormat {
		return nil, fmt.Errorf("transfer_bundle_invalid: unrecognized format %q", bundle.Format)
	}
	return &loadedTransfer{V1Only: true, Config: bundle}, nil
}

func looksLikeZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K'
}

func loadTransferZip(path string) (*loadedTransfer, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = data
	}
	out := &loadedTransfer{Blobs: map[string][]byte{}}
	if raw, ok := files["manifest.json"]; ok {
		if err := json.Unmarshal(raw, &out.Manifest); err != nil {
			return nil, fmt.Errorf("transfer_bundle_invalid: manifest: %w", err)
		}
		if out.Manifest.Format != service.TransferBundleFormat {
			return nil, fmt.Errorf("transfer_bundle_invalid: format %q", out.Manifest.Format)
		}
		// A V3 importer must still read a V2 bundle (schema_version 1): the
		// outer version only says whether the issues group is present.
		if v := out.Manifest.SchemaVersion; v != service.TransferBundleSchemaVersionV1 && v != service.TransferBundleSchemaVersionV2 {
			return nil, fmt.Errorf("transfer_bundle_version_unsupported")
		}
		for _, meta := range out.Manifest.Files {
			body, ok := files[meta.Path]
			if !ok {
				return nil, fmt.Errorf("transfer_bundle_corrupt: missing %s", meta.Path)
			}
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != meta.SHA256 {
				return nil, fmt.Errorf("transfer_bundle_corrupt: sha256 mismatch for %s", meta.Path)
			}
		}
	}
	if raw, ok := files["config.json"]; ok {
		if err := json.Unmarshal(raw, &out.Config); err != nil {
			return nil, fmt.Errorf("config.json: %w", err)
		}
	}
	if raw, ok := files["people.json"]; ok {
		_ = json.Unmarshal(raw, &out.People)
	}
	if raw, ok := files["runtime_profiles.json"]; ok {
		_ = json.Unmarshal(raw, &out.Runtimes)
	}
	if raw, ok := files["preferences.json"]; ok {
		_ = json.Unmarshal(raw, &out.Preferences)
	}
	if raw, ok := files["secrets_omitted.json"]; ok {
		_ = json.Unmarshal(raw, &out.Secrets)
	}
	if raw, ok := files["attachments/index.jsonl"]; ok {
		out.Attachments = decodeJSONL[service.TransferAttachmentRow](raw)
	}
	var sessNames, msgNames, issueNames, commentNames []string
	for name, data := range files {
		if strings.HasPrefix(name, "attachments/blobs/") {
			out.Blobs[strings.TrimPrefix(name, "attachments/blobs/")] = data
		}
		if strings.HasPrefix(name, "conversations/sessions-") {
			sessNames = append(sessNames, name)
		}
		if strings.HasPrefix(name, "conversations/messages-") {
			msgNames = append(msgNames, name)
		}
		if strings.HasPrefix(name, "issues/issues-") {
			issueNames = append(issueNames, name)
		}
		if strings.HasPrefix(name, "issues/comments-") {
			commentNames = append(commentNames, name)
		}
	}
	sort.Strings(sessNames)
	sort.Strings(msgNames)
	sort.Strings(issueNames)
	sort.Strings(commentNames)
	for _, name := range sessNames {
		out.SessionShards = append(out.SessionShards, decodeJSONL[service.TransferSessionRow](files[name]))
	}
	for _, name := range msgNames {
		out.MessageShards = append(out.MessageShards, decodeJSONL[service.TransferMessageRow](files[name]))
	}
	for i, name := range issueNames {
		out.IssueShards = append(out.IssueShards, decodeJSONL[service.TransferIssueRow](files[name]))
		// The exporter always writes one comments file per issues file. A
		// hand-made bundle may not, and a missing file means an issue with no
		// comments rather than a corrupt package.
		if i < len(commentNames) {
			out.CommentShards = append(out.CommentShards, decodeJSONL[service.TransferCommentRow](files[commentNames[i]]))
		} else {
			out.CommentShards = append(out.CommentShards, nil)
		}
	}
	if raw, ok := files["issues/relations.jsonl"]; ok {
		out.Relations = decodeJSONL[service.TransferRelationRow](raw)
	}
	return out, nil
}

func decodeJSONL[T any](data []byte) []T {
	dec := json.NewDecoder(bytes.NewReader(data))
	var out []T
	for {
		var row T
		if err := dec.Decode(&row); err != nil {
			break
		}
		out = append(out, row)
	}
	return out
}

func resolveTransferWorkspaceID(ctx context.Context, src sourceClient, ref string) (string, error) {
	var list []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := src.GetJSON(ctx, "/api/workspaces", &list); err != nil {
		return "", err
	}
	for _, w := range list {
		if w.ID == ref || w.Slug == ref {
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("workspace %q not found", ref)
}

// fetchTransferIssueCounter reads the target's issue-number watermark, which is
// the offset `--renumber` adds to every imported number (contract §2.3).
//
// The watermark is `workspace.issue_counter`, not `MAX(number)`: deleting the
// top issues leaves the counter above the highest surviving row, and an offset
// taken from MAX(number) would place the import inside the range the next
// create is about to allocate — a unique-constraint failure on that later
// create, long after this import reported success.
func fetchTransferIssueCounter(ctx context.Context, client *cli.APIClient, wsID string) (int32, error) {
	var ws struct {
		IssueCounter int32 `json:"issue_counter"`
	}
	if err := (sourceClient{api: client}).GetJSON(ctx, "/api/workspaces/"+url.PathEscape(wsID), &ws); err != nil {
		return 0, err
	}
	return ws.IssueCounter, nil
}

func postTransferAttachment(ctx context.Context, client *cli.APIClient, path string, meta service.TransferAttachmentRow, blob []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaJSON, _ := json.Marshal(service.TransferAttachmentMeta{
		SourceID:          meta.SourceID,
		ChatSessionID:     meta.ChatSessionID,
		ChatMessageID:     meta.ChatMessageID,
		IssueID:           meta.IssueID,
		CommentID:         meta.CommentID,
		Filename:          meta.Filename,
		ContentType:       meta.ContentType,
		SizeBytes:         meta.SizeBytes,
		CreatedAt:         meta.CreatedAt,
		SHA256:            meta.SHA256,
		BodyOmittedReason: meta.BodyOmittedReason,
	})
	if err := mw.WriteField("meta", string(metaJSON)); err != nil {
		return err
	}
	if len(blob) > 0 {
		part, err := mw.CreateFormFile("file", meta.Filename)
		if err != nil {
			return err
		}
		if _, err := part.Write(blob); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(client.BaseURL, "/")+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	if client.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", client.WorkspaceID)
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func httpStatusOf(err error) int {
	var he *cli.HTTPError
	if errors.As(err, &he) {
		return he.StatusCode
	}
	var te *service.TransferHTTPError
	if errors.As(err, &te) {
		return te.Status
	}
	return 0
}

func boolPtr(v bool) *bool { return &v }
