package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var attachmentCmd = &cobra.Command{
	Use:   "attachment",
	Short: "Work with attachments",
}

var attachmentDownloadCmd = &cobra.Command{
	Use:   "download [attachment-id]",
	Short: "Download a verified attachment or all attachments on a comment/issue",
	Long: `Download checksummed attachment bytes to local files. Select one attachment
ID, --comment <id>, or --issue <id-or-identifier>. With --issue and --comment
together, download only that comment's attachments on the issue.
Read real attachment/comment IDs from issue get or issue comment list JSON.
The server enforces the current account/workspace's issue visibility and file access.

Use -o/--output-dir for the destination directory, or --output-file for an
exact path when downloading a single ID. --output json controls the result
format, not the destination. Each file is checked before replacing a destination;
a checksum/size mismatch retries once from authenticated raw storage.
Batch downloads disambiguate repeated filenames with the attachment ID and
return downloaded files and per-file failures as JSON. Successful files remain
available if another file fails; the command exits nonzero for any failure.`,
	Example: `  # Download an image attachment to the current directory
  $ multica attachment download abc123

  # Download to a directory inside the working directory (keep agent
  # downloads out of /tmp and other machine-shared paths, MUL-4252)
  $ multica attachment download abc123 -o ./attachments
  $ multica attachment download abc123 --output-file ./reports/report.pdf
  $ multica attachment download --comment <comment-id> -o ./attachments
  $ multica attachment download --issue DENE-123 -o ./attachments`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAttachmentDownload,
}

var attachmentUploadCmd = &cobra.Command{
	Use:   "upload <path>",
	Short: "Upload a checksummed file to an issue or your chat reply",
	Long: `Upload a local file so it is attached to the reply of the current chat task.

Intended for agents running inside a chat task: the file is tagged with the
task and, when the task completes, the server binds it to the assistant reply
it produces — it appears as an attachment card below your reply even if you
paste nothing. The command also returns a markdown snippet you may paste on its
own line to place the item: files use !file[name](url) (a card), images use
![name](url) (inline).

The task id is read from MULTICA_TASK_ID (set by the daemon inside a task);
override it with --task when needed. Outside a chat task (including external
Bots), use --issue <id> to upload to an authorized issue. No comment is posted.

Inside a daemon task the uploaded file also stays on this machine: the daemon
keeps a copy under <profile>/outputs/<attachment-id>/ so the desktop app here
can open it, or show it in Finder, without downloading it. Comment and issue
--attachment uploads do the same. Copies are removed after
MULTICA_GC_OUTPUTS_TTL (default 14d); the server is not told about them.`,
	Example: `  # Attach an image to the current chat reply
  $ multica attachment upload ./chart.png`,
	Args: exactArgs(1),
	RunE: runAttachmentUpload,
}

func init() {
	attachmentCmd.AddCommand(attachmentDownloadCmd)
	attachmentCmd.AddCommand(attachmentUploadCmd)

	attachmentDownloadCmd.Flags().StringP("output-dir", "o", ".", "Directory to save the downloaded file")
	attachmentDownloadCmd.Flags().String("output-file", "", "Exact destination path for a single attachment (overrides -o/--output-dir)")
	attachmentDownloadCmd.Flags().String("comment", "", "Download all attachments on a comment ID")
	attachmentDownloadCmd.Flags().String("issue", "", "Download all attachments on an issue ID or identifier")
	attachmentDownloadCmd.Flags().String("output", "json", "Result format: json (destination is -o/--output-dir or --output-file)")
	attachmentUploadCmd.Flags().String("task", "", "Chat task id to attach to (defaults to MULTICA_TASK_ID)")
	attachmentUploadCmd.Flags().String("issue", "", "Issue ID or identifier; works with Bot PATs, mutually exclusive with --task")
}

func runAttachmentUpload(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	issueInput, _ := cmd.Flags().GetString("issue")
	taskID, _ := cmd.Flags().GetString("task")
	if issueInput != "" && taskID != "" {
		return fmt.Errorf("--issue and --task are mutually exclusive")
	}
	if taskID == "" {
		taskID = client.TaskID
	}
	if taskID == "" && issueInput == "" {
		return fmt.Errorf("no chat task in context: run inside a chat task (MULTICA_TASK_ID set) or pass --task <id> / --issue <id>")
	}

	path := args[0]
	if isHTTPURL(path) {
		return fmt.Errorf("upload accepts a local file path, not a URL: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file %s: %w", path, err)
	}

	// A 2 MiB chunk keeps every request below proxy limits, but a slow uplink
	// can still take minutes for a full attachment. The resumable client keeps
	// progress server-side, so the command deadline should cover a realistic
	// large-file transfer rather than the historical single-request budget.
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(10*time.Minute))
	defer cancel()

	var att cli.AttachmentResponse
	if issueInput != "" {
		ref, resolveErr := resolveIssueRef(ctx, client, issueInput)
		if resolveErr != nil {
			return resolveErr
		}
		att, err = client.UploadIssueAttachment(ctx, data, path, ref.ID)
	} else {
		att, err = client.UploadChatAttachment(ctx, data, path, taskID)
	}
	if err != nil {
		return fmt.Errorf("upload attachment: %w", err)
	}

	filename := filepath.Base(path)
	markdown := attachmentMarkdown(filename, att.ContentType, att.MarkdownURL)
	fmt.Fprintln(os.Stderr, "Uploaded:", filename)

	return cli.PrintJSON(os.Stdout, map[string]any{
		"id":           att.ID,
		"filename":     filename,
		"markdown_url": att.MarkdownURL,
		"markdown":     markdown,
		"sha256":       att.SHA256,
		"size_bytes":   att.SizeBytes,
	})
}

// attachmentMarkdown renders the markdown snippet that makes an uploaded file
// visible in a body: images inline via `![...]( )`, every other file as a
// block-level attachment card via `!file[...]( )`. The label is escaped so a
// name like `report[v2].pdf` stays one label instead of truncating the
// snippet. Shared by `attachment upload` (prints it for the agent to paste)
// and `issue create --attachment` (appends it to the description).
func attachmentMarkdown(filename, contentType, markdownURL string) string {
	label := escapeMarkdownLabel(filename)
	if strings.HasPrefix(contentType, "image/") {
		return fmt.Sprintf("![%s](%s)", label, markdownURL)
	}
	return fmt.Sprintf("!file[%s](%s)", label, markdownURL)
}

// escapeMarkdownLabel escapes the metacharacters a markdown link/image label
// may not contain unescaped ([ ] ( ) and backslash), so a filename like
// `report[v2].pdf` stays a single valid label instead of truncating the
// snippet. Kept in sync with the renderers' unescape set
// (packages/ui/markdown/file-cards.ts).
func escapeMarkdownLabel(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`[`, `\[`,
		`]`, `\]`,
		`(`, `\(`,
		`)`, `\)`,
	).Replace(s)
}

type attachmentDownloadResult struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func runAttachmentDownload(cmd *cobra.Command, args []string) error {
	commentID, _ := cmd.Flags().GetString("comment")
	issueInput, _ := cmd.Flags().GetString("issue")
	outputFile, _ := cmd.Flags().GetString("output-file")
	format, _ := cmd.Flags().GetString("output")
	if format != "" && format != "json" {
		return fmt.Errorf("--output accepts json; use -o/--output-dir or --output-file for the destination")
	}
	batch := commentID != "" || issueInput != ""
	if (len(args) == 0 && !batch) || (len(args) > 0 && batch) {
		return fmt.Errorf("select one attachment ID, --comment <id>, or --issue <id-or-identifier>")
	}
	if batch && outputFile != "" {
		return fmt.Errorf("--output-file requires a single attachment ID; use -o/--output-dir for batches")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(10*time.Minute))
	defer cancel()
	if !batch {
		result, err := downloadAttachment(ctx, client, args[0], cmd, "")
		if err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, result)
	}
	var attachments []cli.AttachmentResponse
	if issueInput != "" {
		ref, err := resolveIssueRef(ctx, client, issueInput)
		if err != nil {
			return err
		}
		if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/attachments", &attachments); err != nil {
			return err
		}
	} else {
		if err := client.GetJSON(ctx, "/api/comments/"+url.PathEscape(commentID)+"/attachments", &attachments); err != nil {
			return err
		}
	}
	// Reserve every original name before assigning duplicates: an existing
	// report-<id>.pdf must not be overwritten by a renamed report.pdf.
	names := make(map[string]int)
	used := make(map[string]bool)
	for _, att := range attachments {
		if commentID != "" && issueInput != "" && att.CommentID != commentID {
			continue
		}
		name := attachmentFilename(att.Filename, att.ID)
		names[strings.ToLower(name)]++
		used[strings.ToLower(name)] = true
	}
	results := make([]attachmentDownloadResult, 0)
	failures := make([]map[string]string, 0)
	seen := make(map[string]bool)
	for _, att := range attachments {
		if commentID != "" && issueInput != "" && att.CommentID != commentID {
			continue
		}
		if seen[att.ID] {
			continue
		}
		seen[att.ID] = true
		name := attachmentFilename(att.Filename, att.ID)
		if names[strings.ToLower(name)] > 1 {
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext) + "-" + filepath.Base(att.ID)
			name = stem + ext
			for i := 2; used[strings.ToLower(name)]; i++ {
				name = fmt.Sprintf("%s-%d%s", stem, i, ext)
			}
			used[strings.ToLower(name)] = true
		}
		result, err := downloadAttachment(ctx, client, att.ID, cmd, name)
		if err != nil {
			failures = append(failures, map[string]string{"id": att.ID, "error": err.Error()})
			continue
		}
		results = append(results, result)
	}
	if err := cli.PrintJSON(os.Stdout, map[string]any{"files": results, "failures": failures}); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d attachment downloads failed; %d files saved", len(failures), len(results))
	}
	return nil
}

func attachmentFilename(name, id string) string {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		return filepath.Base(id)
	}
	return name
}

func attachmentDownloadURLType(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "invalid"
	}
	if u.Query().Get("raw") == "1" {
		return "authenticated raw storage"
	}
	if !u.IsAbs() {
		return "relative API/storage"
	}
	if u.RawQuery != "" {
		return "signed/absolute storage"
	}
	return "absolute storage/CDN"
}

func verifyAttachmentBytes(att cli.AttachmentResponse, data []byte, downloadURL string) error {
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != att.SHA256 || int64(len(data)) != att.SizeBytes {
		kind := "sha256"
		if actual == att.SHA256 {
			kind = "size"
		}
		return fmt.Errorf("attachment %s mismatch: expected sha256=%s size=%d; actual sha256=%s size=%d; download address type=%s; downloaded bytes discarded", kind, att.SHA256, att.SizeBytes, actual, len(data), attachmentDownloadURLType(downloadURL))
	}
	return nil
}

func downloadAttachment(ctx context.Context, client *cli.APIClient, id string, cmd *cobra.Command, batchName string) (attachmentDownloadResult, error) {
	var result attachmentDownloadResult
	// Typed metadata preserves integer sizes; map[string]any converted large
	// sizes to scientific notation, which ParseInt rejected (DENE-1864).
	var att cli.AttachmentResponse
	if err := client.GetJSON(ctx, "/api/attachments/"+url.PathEscape(id), &att); err != nil {
		return result, fmt.Errorf("get attachment: %w", err)
	}
	downloadURL := att.RawDownloadURL
	if downloadURL == "" {
		downloadURL = att.DownloadURL
	}
	if downloadURL == "" {
		return result, fmt.Errorf("attachment has no download URL")
	}
	if att.SHA256 == "" {
		return result, fmt.Errorf("attachment has no sha256; server must provide a verified checksum")
	}
	data, err := client.DownloadFile(ctx, downloadURL)
	if err != nil {
		return result, fmt.Errorf("download file (%s): %w", attachmentDownloadURLType(downloadURL), err)
	}
	if firstErr := verifyAttachmentBytes(att, data, downloadURL); firstErr != nil {
		fmt.Fprintln(os.Stderr, firstErr.Error()+"; retrying once from raw storage")
		rawURL := "/api/attachments/" + url.PathEscape(id) + "/download?raw=1"
		data, err = client.DownloadFile(ctx, rawURL)
		if err != nil {
			return result, fmt.Errorf("%v; raw download retry: %w", firstErr, err)
		}
		if err = verifyAttachmentBytes(att, data, rawURL); err != nil {
			return result, fmt.Errorf("%v; raw download retry failed: %w; check the stored object and attachment metadata before retrying", firstErr, err)
		}
	}
	filename := attachmentFilename(att.Filename, id)
	if batchName != "" {
		filename = batchName
	}
	outputDir, _ := cmd.Flags().GetString("output-dir")
	destPath := filepath.Join(outputDir, filename)
	if outputFile, _ := cmd.Flags().GetString("output-file"); outputFile != "" {
		destPath = outputFile
		filename = filepath.Base(destPath)
	}
	outputDir = filepath.Dir(destPath)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return result, fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.CreateTemp(outputDir, ".multica-download-*")
	if err != nil {
		return result, fmt.Errorf("write file: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return result, fmt.Errorf("write file: %w", err)
	}
	if err = file.Close(); err != nil {
		return result, fmt.Errorf("close file: %w", err)
	}
	if err = os.Rename(file.Name(), destPath); err != nil {
		return result, fmt.Errorf("save file: %w", err)
	}
	abs, err := filepath.Abs(destPath)
	if err != nil {
		abs = destPath
	}
	fmt.Fprintln(os.Stderr, "Downloaded:", abs)
	return attachmentDownloadResult{ID: att.ID, Filename: filename, Path: abs, Size: att.SizeBytes, SHA256: att.SHA256}, nil
}
