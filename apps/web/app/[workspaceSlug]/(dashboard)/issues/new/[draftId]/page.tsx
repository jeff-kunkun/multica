"use client";

import { use } from "react";
import { IssueDraftPage } from "@multica/views/issues/draft";

/**
 * One requirement-alignment conversation, addressed by its draft id.
 *
 * A real route rather than a modal step: the conversation is a durable
 * server-side object that is refreshed into, linked to and resumed later, so it
 * needs an address of its own.
 */
export default function IssueDraftRoute({
  params,
}: {
  params: Promise<{ draftId: string }>;
}) {
  const { draftId } = use(params);
  return <IssueDraftPage draftId={draftId} />;
}
