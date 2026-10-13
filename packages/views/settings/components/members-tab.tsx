"use client";

import { useCallback, useState, type ReactNode } from "react";
import {
  Copy,
  Crown,
  Link,
  Mail,
  MoreHorizontal,
  Plus,
  Search,
  Trash2,
  User,
  UserMinus,
  X,
} from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import { useOptionalNavigation } from "../../navigation";
import type {
  Invitation,
  MemberRole,
  MemberWithUser,
  ShareLink,
} from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@multica/ui/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import {
  invitationListOptions,
  memberListOptions,
  shareLinkListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { api } from "@multica/core/api";
import { useLocale, useT } from "../../i18n";
import {
  SettingsCard,
  SettingsReadOnlyNotice,
  SettingsTab,
  SettingsViewTabs,
} from "./settings-layout";
import { resolveSettingsLocation, settingsHref } from "./settings-navigation";

type MembersView = "members" | "invitations" | "links";

const ROLE_ICONS: Record<MemberRole, typeof Crown> = {
  owner: Crown,
  member: User,
};

// Builds the shareable URL for a share-link invite. Prefers the navigation
// adapter's getShareableUrl (works on desktop where window.location.origin is
// not the public web origin), falling back to the browser origin on web.
function buildShareLinkUrl(
  navigation: ReturnType<typeof useOptionalNavigation>,
  code: string,
): string {
  const joinPath = `/join?code=${code}`;
  if (navigation?.getShareableUrl) {
    return navigation.getShareableUrl(joinPath);
  }
  return `${typeof window !== "undefined" ? window.location.origin : ""}${joinPath}`;
}

function useRoleLabels() {
  const { t } = useT("settings");
  return {
    owner: {
      label: t(($) => $.members.roles.owner.label),
      icon: ROLE_ICONS.owner,
    },
    member: {
      label: t(($) => $.members.roles.member.label),
      icon: ROLE_ICONS.member,
    },
  } as const;
}

function MemberRow({
  member,
  canManage,
  isSelf,
  busy,
  onRemove,
}: {
  member: MemberWithUser;
  canManage: boolean;
  isSelf: boolean;
  busy: boolean;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const roleConfig = useRoleLabels();
  const rc = roleConfig[member.role];
  const RoleIcon = rc.icon;
  // The owner cannot be removed and cannot leave; members can be removed by
  // the owner.
  const canRemove = canManage && !isSelf && member.role !== "owner";

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <ActorAvatar actorType="member" actorId={member.user_id} size="lg" />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-body font-medium">{member.name}</span>
          {isSelf ? (
            <Badge variant="secondary" className="shrink-0">
              {t(($) => $.members.you)}
            </Badge>
          ) : null}
        </div>
        <div className="truncate text-caption text-muted-foreground">{member.email}</div>
      </div>
      <span className="hidden w-32 shrink-0 text-caption tabular-nums text-muted-foreground md:block">
        {t(($) => $.members.joined, {
          date: new Date(member.created_at).toLocaleDateString(locale),
        })}
      </span>
      <span className="flex w-24 shrink-0 items-center gap-1.5 text-body">
        <RoleIcon className="size-3.5 text-muted-foreground" aria-hidden="true" />
        {rc.label}
      </span>
      <span className="flex size-7 shrink-0 items-center justify-center">
      {canRemove && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                disabled={busy}
                aria-label={t(($) => $.members.member_actions, { name: member.name })}
              >
                <MoreHorizontal className="h-4 w-4 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-auto">
            <DropdownMenuItem variant="destructive" onClick={onRemove}>
              <UserMinus className="h-3.5 w-3.5" />
              {t(($) => $.members.remove_action)}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      </span>
    </div>
  );
}

function InvitationRow({
  invitation,
  canManage,
  onRevoke,
  busy,
}: {
  invitation: Invitation;
  canManage: boolean;
  onRevoke: () => void;
  busy: boolean;
}) {
  const { t } = useT("settings");
  const locale = useLocale();

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
        <Mail className="size-4 text-muted-foreground" aria-hidden="true" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-body font-medium">{invitation.invitee_email}</div>
        <div className="truncate text-caption text-muted-foreground">
          {t(($) => $.members.invitation_meta, {
            sent: new Date(invitation.created_at).toLocaleDateString(locale),
            expires: new Date(invitation.expires_at).toLocaleDateString(locale),
          })}
        </div>
      </div>
      <span className="flex size-7 shrink-0 items-center justify-center">
        {canManage && (
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={busy}
            onClick={onRevoke}
            aria-label={t(($) => $.members.revoke_invitation_aria, {
              email: invitation.invitee_email,
            })}
          >
            <X className="size-4 text-muted-foreground" />
          </Button>
        )}
      </span>
    </div>
  );
}

function ShareLinkRow({
  link,
  onRevoke,
  busy,
  onCopy,
}: {
  link: ShareLink;
  onRevoke: () => void;
  busy: boolean;
  onCopy: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const navigation = useOptionalNavigation();
  const joinUrl = buildShareLinkUrl(navigation, link.code);

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
        <Link className="size-4 text-muted-foreground" aria-hidden="true" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-1 text-body font-medium">
          <span>{t(($) => $.members.share_link_uses, { used: link.use_count, max: link.max_uses ?? "∞" })}</span>
          {link.expires_at && <span>· {t(($) => $.members.share_link_expires, { date: new Date(link.expires_at).toLocaleDateString(locale) })}</span>}
        </div>
        <div
          className="truncate font-mono text-caption text-muted-foreground"
          title={joinUrl}
        >
          {joinUrl}
        </div>
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={onCopy}
        aria-label={t(($) => $.members.share_link_copy_tooltip)}
      >
        <Copy className="size-4 text-muted-foreground" />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        disabled={busy}
        onClick={onRevoke}
        aria-label={t(($) => $.members.share_link_revoke_tooltip)}
      >
        <Trash2 className="size-4 text-muted-foreground" />
      </Button>
    </div>
  );
}

export function MembersTab() {
  const { t } = useT("settings");
  const user = useAuthStore((s) => s.user);
  const workspace = useCurrentWorkspace();
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const navigation = useOptionalNavigation();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: invitations = [] } = useQuery(invitationListOptions(wsId));

  const [inviteOpen, setInviteOpen] = useState(false);
  const [shareLinkOpen, setShareLinkOpen] = useState(false);
  const requestedView = resolveSettingsLocation(
    navigation?.searchParams ?? new URLSearchParams(),
  ).section;
  const urlView: MembersView =
    requestedView === "invitations" || requestedView === "links"
      ? requestedView
      : "members";
  const [localView, setLocalView] = useState<MembersView>(urlView);
  // With a router the URL owns the open list, so search results that land on
  // this page and back/forward switch it too. Without one it is local state.
  const view = navigation ? urlView : localView;
  const setView = (next: MembersView) => {
    if (!navigation) {
      setLocalView(next);
      return;
    }
    navigation.replace(
      settingsHref(navigation.pathname, navigation.searchParams, "members", {
        section: next === "members" ? undefined : next,
      }),
    );
  };
  const [memberQuery, setMemberQuery] = useState("");
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteLoading, setInviteLoading] = useState(false);
  const [memberActionId, setMemberActionId] = useState<string | null>(null);
  const [invitationActionId, setInvitationActionId] = useState<string | null>(null);
  const [shareLinkActionId, setShareLinkActionId] = useState<string | null>(null);
  const [shareLinkLoading, setShareLinkLoading] = useState(false);
  const [shareLinkExpiry, setShareLinkExpiry] = useState<string>("168"); // default 7 days
  const [confirmAction, setConfirmAction] = useState<{
    title: string;
    description: string;
    variant?: "destructive";
    onConfirm: () => Promise<void>;
  } | null>(null);

  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManageWorkspace = currentMember?.role === "owner";
  // Only the owner may list share links; skip the request for plain
  // members (the server would 403) once the current member's role is known.
  const { data: shareLinks = [] } = useQuery(shareLinkListOptions(wsId, canManageWorkspace));
  const needle = memberQuery.trim().toLocaleLowerCase();
  const visibleMembers = needle
    ? members.filter(
        (m) =>
          m.name.toLocaleLowerCase().includes(needle) ||
          m.email.toLocaleLowerCase().includes(needle),
      )
    : members;

  const sendInvitation = useCallback(
    async (email: string) => {
      if (!workspace) return;
      await api.createMember(workspace.id, { email });
      setInviteEmail("");
      await qc.invalidateQueries({
        queryKey: workspaceKeys.invitations(wsId),
      });
      toast.success(t(($) => $.members.toast_invitation_sent));
    },
    [qc, t, workspace, wsId],
  );

  const handleInviteMember = async () => {
    if (!workspace) return;
    const email = inviteEmail.trim();
    setInviteLoading(true);
    try {
      await sendInvitation(email);
      setInviteOpen(false);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_invitation_failed));
    } finally {
      setInviteLoading(false);
    }
  };


  const handleRevokeInvitation = (invitation: Invitation) => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.members.revoke_invitation_title),
      description: t(($) => $.members.revoke_invitation_description, { email: invitation.invitee_email }),
      variant: "destructive",
      onConfirm: async () => {
        setInvitationActionId(invitation.id);
        try {
          await api.revokeInvitation(workspace.id, invitation.id);
          qc.invalidateQueries({ queryKey: workspaceKeys.invitations(wsId) });
          toast.success(t(($) => $.members.toast_invitation_revoked));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_invitation_revoke_failed));
        } finally {
          setInvitationActionId(null);
        }
      },
    });
  };

  const handleRemoveMember = (member: MemberWithUser) => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.members.remove_member_title, { name: member.name }),
      description: t(($) => $.members.remove_member_description, { name: member.name, workspace: workspace.name }),
      variant: "destructive",
      onConfirm: async () => {
        setMemberActionId(member.id);
        try {
          await api.deleteMember(workspace.id, member.id);
          qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
          toast.success(t(($) => $.members.toast_member_removed));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_member_remove_failed));
        } finally {
          setMemberActionId(null);
        }
      },
    });
  };

  const handleCreateShareLink = async () => {
    if (!workspace) return;
    setShareLinkLoading(true);
    try {
      await api.createShareLink(workspace.id, { expires_in: parseInt(shareLinkExpiry) || undefined });
      qc.invalidateQueries({ queryKey: workspaceKeys.shareLinks(wsId) });
      setShareLinkOpen(false);
      toast.success(t(($) => $.members.toast_share_link_created));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_share_link_failed));
    } finally {
      setShareLinkLoading(false);
    }
  };

  const handleRevokeShareLink = (link: ShareLink) => {
    if (!workspace) return;
    setShareLinkActionId(link.id);
    api.revokeShareLink(workspace.id, link.id)
      .then(() => {
        qc.invalidateQueries({ queryKey: workspaceKeys.shareLinks(wsId) });
        toast.success(t(($) => $.members.toast_share_link_revoked));
      })
      .catch((e) => {
        toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_share_link_revoke_failed));
      })
      .finally(() => setShareLinkActionId(null));
  };

  const handleCopyShareLink = (link: ShareLink) => {
    const joinUrl = buildShareLinkUrl(navigation, link.code);
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(joinUrl).then(
        () => toast.success(t(($) => $.members.toast_share_link_copied)),
        () => toast.error(t(($) => $.members.toast_share_link_copy_failed)),
      );
    } else {
      const textArea = document.createElement("textarea");
      textArea.value = joinUrl;
      textArea.style.position = "fixed";
      textArea.style.left = "-9999px";
      document.body.appendChild(textArea);
      textArea.select();
      try {
        document.execCommand("copy");
        toast.success(t(($) => $.members.toast_share_link_copied));
      } catch {
        toast.error(t(($) => $.members.toast_share_link_copy_failed));
      }
      document.body.removeChild(textArea);
    }
  };

  if (!workspace) return null;

  const expiryOptions = [
    { value: "24", label: t(($) => $.members.expiry_24h) },
    { value: "168", label: t(($) => $.members.expiry_7d) },
    { value: "720", label: t(($) => $.members.expiry_30d) },
    { value: "0", label: t(($) => $.members.expiry_never) },
  ];
  const views: { value: MembersView; label: string; count: number }[] = [
    { value: "members", label: t(($) => $.members.members_label), count: members.length },
    { value: "invitations", label: t(($) => $.members.pending_label), count: invitations.length },
    ...(canManageWorkspace
      ? [{ value: "links" as const, label: t(($) => $.members.share_links_label), count: shareLinks.length }]
      : []),
  ];
  const activeView = views.some((item) => item.value === view) ? view : "members";

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.members)}
      scope="workspace"
      actions={
        canManageWorkspace ? (
          <Button onClick={() => setInviteOpen(true)}>
            <Plus />
            {t(($) => $.members.invite_title)}
          </Button>
        ) : undefined
      }
    >
      {currentMember && !canManageWorkspace ? (
        <SettingsReadOnlyNotice wsId={wsId} />
      ) : null}
      <section className="space-y-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <SettingsViewTabs
            label={t(($) => $.page.tabs.members)}
            items={views}
            value={activeView}
            onChange={setView}
          />
          <div className="flex min-w-0 items-center gap-3">
            {activeView === "members" ? (
              <div className="relative w-full sm:w-56">
                <Search
                  aria-hidden="true"
                  className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
                />
                <Input
                  type="search"
                  value={memberQuery}
                  onChange={(event) => setMemberQuery(event.target.value)}
                  placeholder={t(($) => $.members.search_placeholder)}
                  aria-label={t(($) => $.members.search_placeholder)}
                  className="pl-8"
                />
              </div>
            ) : activeView === "links" ? (
              <Button variant="outline" size="sm" onClick={() => setShareLinkOpen(true)}>
                <Plus />
                {t(($) => $.members.share_links_create_title)}
              </Button>
            ) : null}
          </div>
        </div>

        {activeView === "members" ? (
          visibleMembers.length > 0 ? (
            <SettingsCard>
              {visibleMembers.map((m) => (
                <div key={m.id}>
                  <MemberRow
                    member={m}
                    canManage={canManageWorkspace}
                    isSelf={m.user_id === user?.id}
                    busy={memberActionId === m.id}
                    onRemove={() => handleRemoveMember(m)}
                  />
                </div>
              ))}
            </SettingsCard>
          ) : (
            <EmptyList>
              {needle ? t(($) => $.members.no_results) : t(($) => $.members.no_members)}
            </EmptyList>
          )
        ) : activeView === "invitations" ? (
          invitations.length > 0 ? (
            <SettingsCard>
              {invitations.map((inv) => (
                <div key={inv.id}>
                  <InvitationRow
                    invitation={inv}
                    canManage={canManageWorkspace}
                    onRevoke={() => handleRevokeInvitation(inv)}
                    busy={invitationActionId === inv.id}
                  />
                </div>
              ))}
            </SettingsCard>
          ) : (
            <EmptyList>{t(($) => $.members.no_invitations)}</EmptyList>
          )
        ) : shareLinks.length > 0 ? (
          <SettingsCard>
            {shareLinks.map((link) => (
              <div key={link.id}>
                <ShareLinkRow
                  link={link}
                  onRevoke={() => handleRevokeShareLink(link)}
                  busy={shareLinkActionId === link.id}
                  onCopy={() => handleCopyShareLink(link)}
                />
              </div>
            ))}
          </SettingsCard>
        ) : (
          <EmptyList>{t(($) => $.members.no_share_links)}</EmptyList>
        )}
      </section>

      <Dialog
        open={inviteOpen}
        onOpenChange={(open) => {
          if (!open && !inviteLoading) setInviteOpen(false);
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.members.invite_title)}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (inviteEmail.trim()) void handleInviteMember();
            }}
          >
            <div className="space-y-1.5">
              <Label htmlFor="invite-email">{t(($) => $.members.email_label)}</Label>
              <Input
                id="invite-email"
                type="email"
                name="invite-email"
                autoComplete="email"
                spellCheck={false}
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
                placeholder={t(($) => $.members.invite_email_placeholder)}
                autoFocus
              />
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setInviteOpen(false)}
                disabled={inviteLoading}
              >
                {t(($) => $.members.confirm_cancel)}
              </Button>
              <Button
                type="submit"
                disabled={inviteLoading || !inviteEmail.trim()}
                aria-busy={inviteLoading || undefined}
              >
                {inviteLoading ? t(($) => $.members.inviting) : t(($) => $.members.invite_button)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog
        open={shareLinkOpen}
        onOpenChange={(open) => {
          if (!open && !shareLinkLoading) setShareLinkOpen(false);
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.members.share_links_create_title)}</DialogTitle>
            <DialogDescription>{t(($) => $.members.share_links_description)}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="share-link-expiry">{t(($) => $.members.expiry_label)}</Label>
              <Select
                items={expiryOptions}
                value={shareLinkExpiry}
                onValueChange={(v) => v && setShareLinkExpiry(v)}
              >
                <SelectTrigger id="share-link-expiry" className="w-full">
                  <SelectValue>
                    {() =>
                      expiryOptions.find((option) => option.value === shareLinkExpiry)?.label ??
                      shareLinkExpiry
                    }
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {expiryOptions.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setShareLinkOpen(false)}
              disabled={shareLinkLoading}
            >
              {t(($) => $.members.confirm_cancel)}
            </Button>
            <Button
              onClick={() => void handleCreateShareLink()}
              disabled={shareLinkLoading}
              aria-busy={shareLinkLoading || undefined}
            >
              {shareLinkLoading
                ? t(($) => $.members.share_links_creating)
                : t(($) => $.members.share_links_create_button)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>


      <AlertDialog open={!!confirmAction} onOpenChange={(v) => { if (!v) setConfirmAction(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmAction?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmAction?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.members.confirm_cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant={confirmAction?.variant === "destructive" ? "destructive" : "default"}
              onClick={async () => {
                await confirmAction?.onConfirm();
                setConfirmAction(null);
              }}
            >
              {t(($) => $.members.confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function EmptyList({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-xl border border-dashed border-surface-border px-4 py-10 text-center text-body text-muted-foreground">
      {children}
    </div>
  );
}
