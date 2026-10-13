import { redirect } from "next/navigation";

// The marketing site is gone. Signed-in visitors are sent to their last
// workspace by proxy.ts; everyone else lands on the login page.
export default function RootPage() {
  redirect("/login");
}
