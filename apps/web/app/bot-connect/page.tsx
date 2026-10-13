"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { BotConnectApprovePage } from "@multica/views/bot-connect";

// /bot-connect?code=XXXX-XXXX is where a Bot's "let me in" link lands
// (DENE-2009). Suspense wraps useSearchParams per Next.js 15's CSR-bailout rule.
function BotConnectPageContent() {
  const searchParams = useSearchParams();
  return <BotConnectApprovePage code={searchParams.get("code")} />;
}

export default function Page() {
  return (
    <Suspense fallback={null}>
      <BotConnectPageContent />
    </Suspense>
  );
}
