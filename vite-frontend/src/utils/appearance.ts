import { useSyncExternalStore } from "react";

export type Appearance = "default" | "flyx";
const key = "ovo:appearance";
const changed = "ovo:appearance-changed";
export function getAppearance(): Appearance {
  try {
    return localStorage.getItem(key) === "flyx" ? "flyx" : "default";
  } catch {
    return "default";
  }
}
export function applyAppearance() {
  const value = getAppearance();
  if (value === "flyx") document.documentElement.dataset.appearance = value;
  else delete document.documentElement.dataset.appearance;
}
export function setAppearance(value: Appearance) {
  try {
    if (value === "flyx") localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    return;
  }
  applyAppearance();
  window.dispatchEvent(new Event(changed));
}
function subscribe(listener: () => void) {
  const update = () => {
    applyAppearance();
    listener();
  };
  const storage = (event: StorageEvent) => {
    if (event.key === key || event.key === null) update();
  };
  window.addEventListener(changed, update);
  window.addEventListener("storage", storage);
  return () => {
    window.removeEventListener(changed, update);
    window.removeEventListener("storage", storage);
  };
}
export function useAppearance() {
  return useSyncExternalStore(
    subscribe,
    getAppearance,
    () => "default" as const,
  );
}
