import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";

interface ConfigState {
  cdnDomain: string;
  attachmentPublicUrl: string;
  // True when cdnDomain serves private content via time-bounded signed URLs
  // (CloudFront signing enabled server-side). Renderers must not treat a raw
  // storage URL on that domain as a loadable media source (MUL-3254).
  cdnSigned: boolean;
  allowSignup: boolean;
  googleClientId: string;
  // Self-host opt-in: login page collects username + password instead of
  // an email verification code. Absent/false keeps the official email flow.
  passwordAuth: boolean;
  // Self-host opt-in: /signup requires a shared team TOTP. Absent/false
  // keeps the previous username + password form with no 2FA field.
  signupTotpRequired: boolean;
  daemonServerUrl: string;
  daemonAppUrl: string;
  // Self-host gate (#3433): when true, every "Create workspace" affordance
  // must be hidden. Defaults to false so unknown / older servers behave like
  // the managed-cloud case.
  workspaceCreationDisabled: boolean;
  // Self-host-only gate for the Git provider integration (Forgejo / Gitea /
  // GitLab). When false the whole Settings → Integrations "Git providers"
  // section is hidden. Defaults to false so unknown / older servers and the
  // managed cloud (which omits the field) keep it hidden.
  vcsIntegrationAvailable: boolean;
  featureFlags: Record<string, boolean>;
  // The running API build version, surfaced in the Help popover so
  // self-hosted operators can confirm what's deployed. Empty for dev builds
  // or servers older than this feature.
  serverVersion: string;
  // Whether this server persists conversation_starters on agent create/update.
  // Older handlers accepted the unknown field and returned success while
  // dropping it, so absent must fail closed.
  agentConversationStartersSupported: boolean;
  // Whether POST /api/issues atomically persists custom-property values.
  // Older servers silently drop the field, so absent must fail closed.
  issueCreatePropertiesSupported: boolean;
  // Whether deleting a comment keeps its replies (#8296). Older servers
  // deleted the replies too, so absent must fail closed: the client then
  // promises nothing about replies and uses the legacy delete route.
  commentDeleteKeepRepliesSupported: boolean;
  setCdnConfig: (config: { cdnDomain: string; cdnSigned?: boolean }) => void;
  setAttachmentPublicUrl: (url?: string) => void;
  setAuthConfig: (config: {
    allowSignup: boolean;
    googleClientId?: string;
    workspaceCreationDisabled?: boolean;
    vcsIntegrationAvailable?: boolean;
    passwordAuth?: boolean;
    signupTotpRequired?: boolean;
  }) => void;
  setDaemonConfig: (config: {
    daemonServerUrl?: string;
    daemonAppUrl?: string;
  }) => void;
  setFeatureFlags: (flags?: Record<string, boolean>) => void;
  setServerVersion: (version?: string) => void;
  setAgentConversationStartersSupported: (supported?: boolean) => void;
  setIssueCreatePropertiesSupported: (supported?: boolean) => void;
  setCommentDeleteKeepRepliesSupported: (supported?: boolean) => void;
}

export const configStore = createStore<ConfigState>((set) => ({
  cdnDomain: "",
  attachmentPublicUrl: "",
  cdnSigned: false,
  allowSignup: true,
  googleClientId: "",
  passwordAuth: false,
  signupTotpRequired: false,
  daemonServerUrl: "",
  daemonAppUrl: "",
  workspaceCreationDisabled: false,
  vcsIntegrationAvailable: false,
  featureFlags: {},
  serverVersion: "",
  agentConversationStartersSupported: false,
  issueCreatePropertiesSupported: false,
  commentDeleteKeepRepliesSupported: false,
  setCdnConfig: ({ cdnDomain, cdnSigned = false }) => set({ cdnDomain, cdnSigned }),
  setAttachmentPublicUrl: (url = "") => set({ attachmentPublicUrl: url }),
  setAuthConfig: ({
    allowSignup,
    googleClientId = "",
    workspaceCreationDisabled = false,
    vcsIntegrationAvailable = false,
    passwordAuth = false,
    signupTotpRequired = false,
  }) => set({ allowSignup, googleClientId, workspaceCreationDisabled, vcsIntegrationAvailable, passwordAuth, signupTotpRequired }),
  setDaemonConfig: ({ daemonServerUrl = "", daemonAppUrl = "" }) =>
    set({ daemonServerUrl, daemonAppUrl }),
  setFeatureFlags: (flags = {}) => set({ featureFlags: { ...flags } }),
  setServerVersion: (version = "") => set({ serverVersion: version }),
  setAgentConversationStartersSupported: (supported = false) =>
    set({ agentConversationStartersSupported: supported === true }),
  setIssueCreatePropertiesSupported: (supported = false) =>
    set({ issueCreatePropertiesSupported: supported === true }),
  setCommentDeleteKeepRepliesSupported: (supported = false) =>
    set({ commentDeleteKeepRepliesSupported: supported === true }),
}));

export function useConfigStore(): ConfigState;
export function useConfigStore<T>(selector: (state: ConfigState) => T): T;
export function useConfigStore<T>(selector?: (state: ConfigState) => T) {
  return useStore(configStore, selector as (state: ConfigState) => T);
}

export function featureFlagEnabled(
  flags: Readonly<Record<string, boolean>> | undefined,
  key: string,
  defaultValue = false,
): boolean {
  return flags?.[key] ?? defaultValue;
}

export function useFeatureEnabled(key: string, defaultValue = false): boolean {
  return useConfigStore((state) =>
    featureFlagEnabled(state.featureFlags, key, defaultValue),
  );
}
