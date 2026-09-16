package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
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

func init() {
	transferExportCmd.Flags().String("workspace", "", "Source workspace slug or id")
	transferExportCmd.Flags().String("out", "", "Output zip path")
	transferExportCmd.Flags().String("include", "config,conversations,attachments", "Comma-separated parts to include")
	transferExportCmd.Flags().Bool("estimate", false, "Estimate size without writing a bundle")
	transferExportCmd.Flags().Bool("exclude-archived", false, "Skip archived chats")
	transferExportCmd.Flags().Bool("no-people", false, "Omit people.json (member refs other than exporter will degrade)")
	_ = transferExportCmd.MarkFlagRequired("workspace")

	transferImportCmd.Flags().String("workspace", "", "Target workspace slug or id")
	transferImportCmd.Flags().String("in", "", "Input zip or V1 JSON path")
	transferImportCmd.Flags().Bool("dry-run", false, "Preview without writing")
	transferImportCmd.Flags().String("on-conflict", "fail", "Conflict policy for config entities: fail, overwrite, rename, skip")
	registerTransferImportOptionFlags(transferImportCmd)
	_ = transferImportCmd.MarkFlagRequired("workspace")
	_ = transferImportCmd.MarkFlagRequired("in")

	transferCmd.AddCommand(transferExportCmd)
	transferCmd.AddCommand(transferImportCmd)
}

// registerTransferImportOptionFlags declares the config-import switches
// `transfer import` forwards to the target. The defaults mirror the Desktop
// migration card: moving a workspace across environments is meant to reproduce
// the same environment, so autopilots come across running, while the issue
// prefix stays put because adopting it changes every later issue key.
func registerTransferImportOptionFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("activate-autopilots", true, "Keep the source autopilot status; imported automations start triggering immediately (--activate-autopilots=false imports them paused)")
	cmd.Flags().Bool("apply-workspace-settings", true, "Apply the exported workspace settings (context, repos, attribution)")
	cmd.Flags().Bool("apply-issue-prefix", false, "Also adopt the exported issue prefix (changes the key of every future issue in the target)")
	cmd.Flags().Bool("auto-bind-runtimes", true, "Bind an imported agent to the target runtime that matches its source runtime, when exactly one matches (--auto-bind-runtimes=false leaves every agent unbound)")
}

// transferImportOptions reads the switches registered above. A command built
// without them falls back to the same defaults.
func transferImportOptions(cmd *cobra.Command) service.ConfigImportOptions {
	activate, _ := cmd.Flags().GetBool("activate-autopilots")
	applySettings, _ := cmd.Flags().GetBool("apply-workspace-settings")
	applyPrefix, _ := cmd.Flags().GetBool("apply-issue-prefix")
	// Auto-bind defaults ON, so read it through Lookup: a command built without
	// the flag must not fall through to GetBool's false and silently import
	// every agent unbound again (DENE-364).
	autoBind := true
	if cmd.Flags().Lookup("auto-bind-runtimes") != nil {
		autoBind, _ = cmd.Flags().GetBool("auto-bind-runtimes")
	}
	return service.ConfigImportOptions{
		ActivateAutopilots:     activate,
		ApplyWorkspaceSettings: &applySettings,
		ApplyIssuePrefix:       applyPrefix,
		AutoBindRuntimes:       &autoBind,
	}
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
	importOptions := transferImportOptions(cmd)

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
	}
	return cli.PrintJSON(cmd.OutOrStdout(), cfgReport)
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
		if out.Manifest.SchemaVersion != service.TransferBundleSchemaVersion {
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
	var sessNames, msgNames []string
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
	}
	sort.Strings(sessNames)
	sort.Strings(msgNames)
	for _, name := range sessNames {
		out.SessionShards = append(out.SessionShards, decodeJSONL[service.TransferSessionRow](files[name]))
	}
	for _, name := range msgNames {
		out.MessageShards = append(out.MessageShards, decodeJSONL[service.TransferMessageRow](files[name]))
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

func postTransferAttachment(ctx context.Context, client *cli.APIClient, path string, meta service.TransferAttachmentRow, blob []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaJSON, _ := json.Marshal(service.TransferAttachmentMeta{
		SourceID:          meta.SourceID,
		ChatSessionID:     meta.ChatSessionID,
		ChatMessageID:     meta.ChatMessageID,
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
