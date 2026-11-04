"use client";

import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@multica/ui/components/ui/card";
import { useT } from "@multica/views/i18n";

export function CliCallbackError() {
  const { t } = useT("auth");
  return (
    <div className="flex min-h-svh flex-col items-center justify-center overflow-y-auto px-4 py-[max(1.5rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))] sm:flex-row">
      <Card className="my-auto w-full max-w-sm" role="alert">
        <CardHeader className="text-center">
          <CardTitle className="text-display-sm">
            {t(($) => $.web.cli_callback.failed_title)}
          </CardTitle>
          <CardDescription>
            {t(($) => $.web.cli_callback.invalid_description)}
          </CardDescription>
        </CardHeader>
      </Card>
    </div>
  );
}
