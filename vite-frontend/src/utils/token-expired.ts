import { clearSession, getToken } from "@/utils/session";

export function handleTokenExpired() {
  if (!getToken()) return;
  clearSession();
  if (window.location.pathname !== "/") window.location.href = "/";
}
