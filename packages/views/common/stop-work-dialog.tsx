"use client";

import { useEffect, useState } from "react";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { useT } from "../i18n";

export function StopWorkDialog({ open, pending, cancelling, onOpenChange, onConfirm }: {
  open: boolean; pending?: boolean; cancelling?: boolean; onOpenChange: (open: boolean) => void;
  onConfirm: (related: boolean) => void;
}) {
  const { t } = useT("common");
  const [related, setRelated] = useState(false);
  useEffect(() => { if (!open) setRelated(false); }, [open]);
  return (
    <AlertDialog open={open} onOpenChange={(next) => {
      if (pending) return;
      if (!next) setRelated(false);
      onOpenChange(next);
    }}>
      <AlertDialogContent onClick={(event) => event.stopPropagation()}>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => cancelling ? $.stop_work.cancel_title : $.stop_work.title)}</AlertDialogTitle>
          <AlertDialogDescription>{t(($) => cancelling ? $.stop_work.cancel_description : $.stop_work.description)}</AlertDialogDescription>
        </AlertDialogHeader>
        <label className="flex min-h-11 cursor-pointer items-center gap-3 text-body">
          <Checkbox checked={related} disabled={pending} onCheckedChange={(checked) => setRelated(checked === true)} />
          <span>{t(($) => $.stop_work.related)}</span>
        </label>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t(($) => cancelling ? $.stop_work.cancel_keep : $.stop_work.keep)}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={pending} onClick={(event) => {
            event.preventDefault();
            onConfirm(related);
          }}>{t(($) => cancelling ? (related ? $.stop_work.cancel_confirm_related : $.stop_work.cancel_confirm) : (related ? $.stop_work.confirm_related : $.stop_work.confirm))}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
