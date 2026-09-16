export { useImmersiveMode } from "./use-immersive-mode";
export { useDesktopUnreadBadge } from "./use-desktop-unread-badge";
export { DragStrip } from "./drag-strip";
export { openExternal } from "./open-external";
export {
  isDesktopShell,
  pickDirectory,
  validateLocalDirectory,
  canSetLocalDirectorySharedOverride,
  listLocalDirectorySharedOverrides,
  setLocalDirectorySharedOverride,
  localDirectoryOverrideKey,
  normalizeLocalDirectoryOverridePath,
  type PickDirectoryResult,
  type ValidateLocalDirectoryResult,
  type LocalDirectorySharedOverride,
  type SetLocalDirectorySharedOverrideResult,
} from "./local-directory";
export {
  pickTransferExportPath,
  pickTransferImportPath,
  runWorkspaceTransfer,
  subscribeTransferProgress,
  fileNameFromPath,
  formatTransferBytes,
  transferExportSourceHost,
  TRANSFER_EXPORT_COMPLETED_KEY,
  hasCompletedTransferExport,
  markTransferExportCompleted,
  type TransferErrorCode,
  type TransferProgressEvent,
  type TransferImportOptions,
  type TransferAutopilotSummary,
  type TransferImportReportView,
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
export { useLocalDirectorySharedOverrides } from "./use-local-directory-shared-overrides";
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
