/** macOS Full Disk Access, as seen by the running Desktop app. */
export interface FullDiskAccessState {
  /** "unsupported" covers non-macOS and Macs where no probe file exists. */
  status: "granted" | "missing" | "unsupported";
  /** The name to pick with "+" in System Settings (e.g. "Multica"). */
  appName: string;
}
