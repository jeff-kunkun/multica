import { useParams } from "react-router-dom";
import { IssueDraftPage as SharedIssueDraftPage } from "@multica/views/issues/draft";

/**
 * The desktop shell for one alignment conversation. The shared page owns the
 * behaviour; this only reads the draft id off the route, which is what makes
 * the conversation restore as a tab and survive a refresh.
 */
export function IssueDraftPage() {
  const { draftId } = useParams<{ draftId: string }>();
  if (!draftId) return null;
  return <SharedIssueDraftPage draftId={draftId} />;
}
