export { useImmersiveMode } from "./use-immersive-mode";
export { useDesktopUnreadBadge } from "./use-desktop-unread-badge";
export { DragStrip } from "./drag-strip";
export { openExternal } from "./open-external";
export {
  isDesktopShell,
  pickDirectory,
  validateLocalDirectory,
  initLocalGit,
  type PickDirectoryResult,
  type ValidateLocalDirectoryResult,
  type InitLocalGitResult,
} from "./local-directory";
export {
  pickTransferExportPath,
  pickTransferImportPath,
  getTransferJobState,
  runWorkspaceTransfer,
  subscribeTransferJobState,
  subscribeTransferProgress,
  fileNameFromPath,
  formatTransferBytes,
  transferExportSourceHost,
  transferProgressRatio,
  TRANSFER_EXPORT_COMPLETED_KEY,
  hasCompletedTransferExport,
  markTransferExportCompleted,
  type TransferErrorCode,
  type TransferJobKind,
  type TransferJobState,
  type TransferProgressEvent,
  type TransferStage,
  type TransferImportOptions,
  type TransferAutopilotSummary,
  type TransferImportReportView,
  type TransferIssuesDegradation,
  type TransferIssuesDegradationKind,
  type TransferIssuesSummary,
  type TransferRunRequest,
  type TransferRunResult,
  type TransferPickPathResult,
  type TransferFlagStorage,
  type TransferBindRuntimesReport,
  type TransferRuntimeBind,
  type TransferRuntimeBindStatus,
  type TransferRuntimeBinding,
  type TransferRuntimeBindingOutcome,
  type TransferRuntimeCandidate,
} from "./workspace-transfer";
export {
  useLocalDaemonStatus,
  type LocalDaemonStatus,
} from "./use-local-daemon-status";
export {
  useFullDiskAccess,
  type FullDiskAccess,
} from "./full-disk-access";
export {
  ScrollRestorationProvider,
  useScrollRestorationAdapter,
  useRestoredScrollEntry,
  useRestoredScrollOffset,
  useRestoredScrollRef,
  useRestoredViewState,
  useViewStateWriter,
  type ExternalScrollSource,
  type ScrollRestorationAdapter,
  type ScrollRestorationEntry,
} from "./scroll-restoration";
