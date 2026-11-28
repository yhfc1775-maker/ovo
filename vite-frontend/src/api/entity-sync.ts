// Mutations refresh dependent list snapshots; read-only POSTs never emit.
export const ENTITY_CHANGED_EVENT = "ovo:entities-changed";
export const ENTITY_REVISION_KEY = "ovo:entity-revision";

export const entityMutationPaths = new Set([
  "config/update", "config/update-single",
  "node/install", "node/check-status", "node/dismiss-expiry-reminder",
  "node/connlog-repush", "node/nftables/reconcile", "node/nftables/clear",
  "node/swap-set", "node/tune", "kernel/install", "kernel/control",
  "monitor/permission/assign", "monitor/permission/remove",
  "user/updatePassword", "backup/restore",
  "node/create", "node/update", "node/delete", "node/batch-delete",
  "node/update-order", "node/batch-set-group", "node/group/create",
  "node/group/rename", "node/group/delete", "node/restart", "node/batch-restart",
  "node/restart-schedule", "node/upgrade", "node/batch-upgrade", "node/rollback",
  "node/upgrade-verification/clear", "node/rotate-secret", "node/latency/targets",
  "tunnel/create", "tunnel/update", "tunnel/delete", "tunnel/batch-delete",
  "tunnel/delete-with-forwards", "tunnel/batch-delete-with-forwards",
  "tunnel/update-order", "tunnel/batch-redeploy", "tunnel/user/assign",
  "tunnel/user/batch-assign", "tunnel/user/update", "tunnel/user/remove",
  "forward/create", "forward/update", "forward/delete", "forward/force-delete",
  "forward/batch-delete", "forward/batch-change-tunnel", "forward/pause",
  "forward/resume", "forward/batch-pause", "forward/batch-resume",
  "forward/reset-flow", "forward/update-order", "forward/batch-redeploy",
  "inbound/create", "inbound/update", "inbound/external/update", "inbound/delete",
  "inbound/batch-delete", "inbound/toggle", "inbound/batch-toggle", "inbound/import",
  "inbound/regenerate", "inbound/reset-traffic", "inbound/split",
  "inbound/relay/create", "inbound/relay/delete",
  "user/create", "user/update", "user/delete", "user/reset", "user/quota/reset",
  "user-inbound/set", "user-subscription/set",
  "speed-limit/create", "speed-limit/update", "speed-limit/delete",
  "subscription/save", "subscription/delete", "subscription/reset",
  "subscription/reset-traffic", "subscription/source/save", "subscription/source/delete",
  "subscription/source/refresh", "subscription/template/save",
  "subscription/template/delete", "subscription/template/refresh", "backup/import",
]);

export function notifyEntityMutation(path: string, code: number): void {
  // -2 may include partially saved changes; refreshing reads never turns
  // the original failure receipt into a success.
  if (code !== 0 && code !== -2) return;
  const route = path.split("?")[0].replace(/^\/?(?:api\/v1\/)*/, "");
  if (!entityMutationPaths.has(route) || typeof window === "undefined") return;
  window.dispatchEvent(new Event(ENTITY_CHANGED_EVENT));
  try {
    // No entity content or credentials cross the storage channel.
    localStorage.setItem(ENTITY_REVISION_KEY, `${Date.now()}:${Math.random()}`);
  } catch {
    // Storage may be disabled; this document's event has already fired.
  }
}
