"use client";

import { Suspense } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { resolvePostAuthDestination } from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";
import { setLoggedInCookie } from "@/features/auth/auth-cookie";
import { SignupPage } from "@multica/views/auth";

function SignupPageContent() {
  const router = useRouter();
  const qc = useQueryClient();

  const handleSuccess = async () => {
    const currentUser = useAuthStore.getState().user;
    const onboarded = currentUser?.onboarded_at != null;
    const list = qc.getQueryData<Workspace[]>(workspaceKeys.list()) ?? [];
    router.push(resolvePostAuthDestination(list, onboarded));
  };

  return (
    <SignupPage
      onSuccess={handleSuccess}
      onTokenObtained={setLoggedInCookie}
    />
  );
}

export default function Page() {
  return (
    <Suspense fallback={null}>
      <SignupPageContent />
    </Suspense>
  );
}
