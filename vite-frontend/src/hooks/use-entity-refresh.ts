import { useEffect, useRef } from "react";
import { ENTITY_CHANGED_EVENT, ENTITY_REVISION_KEY } from "@/api/entity-sync";

// Refresh list data, never remount a page or replace an open editor draft.
export function useEntityRefresh(refresh: () => unknown) {
  const current = useRef(refresh);
  current.current = refresh;
  useEffect(() => {
    let active = true;
    let pending = false;
    const changed = () => {
      if (document.visibilityState === "hidden") { pending = true; return; }
      pending = false;
      void Promise.resolve().then(() => { if (active) return current.current(); }).catch(() => undefined);
    };
    const storage = (event: StorageEvent) => {
      if (event.key === ENTITY_REVISION_KEY) changed();
    };
    const resume = () => { if (pending) changed(); };
    window.addEventListener(ENTITY_CHANGED_EVENT, changed);
    window.addEventListener("storage", storage);
    window.addEventListener("focus", resume);
    document.addEventListener("visibilitychange", resume);
    return () => {
      active = false;
      window.removeEventListener(ENTITY_CHANGED_EVENT, changed);
      window.removeEventListener("storage", storage);
      window.removeEventListener("focus", resume);
      document.removeEventListener("visibilitychange", resume);
    };
  }, []);
}
