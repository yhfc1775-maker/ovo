import * as React from "react";
import { getToken, SESSION_CLEARED_EVENT, SESSION_UPDATED_EVENT, SESSION_STORAGE_KEYS } from "@/utils/session";
import { ENTITY_CHANGED_EVENT, ENTITY_REVISION_KEY } from "@/api/entity-sync";
import { useHref, useNavigate } from "react-router-dom";
import { Toaster } from "react-hot-toast";
import { I18nProvider } from "@react-aria/i18n";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { HeroUIProvider } from "@/shadcn-bridge/heroui/system";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30000, // 30s
      gcTime: 5 * 60 * 1000, // 5min
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

export interface ProvidersProps {
  children: React.ReactNode;
}

// Shared toast surface for every toast type: the per-type style in
// react-hot-toast replaces rather than merges, so each type spreads this.
// Matte glass (2026-11-04): --toaster-bg is the surface-tier glass
// gradient (stops 0.85 -> 0.75 -> 0.70) and --toaster-color the
// near-black foreground, so toasts follow the site-wide matte-glass
// recipe like every floating surface, with the surface-tier blur.
const toastSurfaceStyle = {
  background: "var(--toaster-bg)",
  color: "var(--toaster-color)",
  border: "1px solid var(--toaster-border)",
  borderRadius: 12,
  boxShadow: "0 6px 20px rgb(24 24 27 / 0.08)",
  backdropFilter: "blur(18px) saturate(150%)",
  WebkitBackdropFilter: "blur(18px) saturate(150%)",
} as const;

export function Provider({ children }: ProvidersProps) {
  const navigate = useNavigate();
  const currentToken = React.useRef(getToken());
  const [sessionRevision, setSessionRevision] = React.useState(0);
  React.useEffect(() => {
    const clearQueries = () => {
      // Reset observed results as well as removing cache entries. A mounted
      // observer must not keep rendering the previous account's rows.
      for (const query of queryClient.getQueryCache().getAll()) query.reset();
      queryClient.clear();
      setSessionRevision((revision) => revision + 1);
    };
    const session = () => {
      const next = getToken();
      if (next !== currentToken.current) {
        currentToken.current = next;
        clearQueries();
      }
    };
    const cleared = () => { currentToken.current = getToken(); clearQueries(); };
    const changed = () => {
      // Query's first fetch has no data to cancel/refetch yet. If a mutation
      // intersects it, refetch those still-observed queries after it settles.
      const initial = new Set(queryClient.getQueryCache().getAll().filter(
        (query) => query.state.fetchStatus === "fetching" && query.state.data === undefined,
      ));
      void queryClient.invalidateQueries().then(() => {
        if (initial.size > 0) return queryClient.invalidateQueries({ predicate: (query) => initial.has(query) });
      });
    };
    const storage = (event: StorageEvent) => {
      if (event.key === SESSION_STORAGE_KEYS.token || event.key === null) {
        session();
        window.dispatchEvent(new Event(SESSION_UPDATED_EVENT));
      }
      else if (event.key === ENTITY_REVISION_KEY) changed();
    };
    window.addEventListener(SESSION_UPDATED_EVENT, session);
    window.addEventListener(SESSION_CLEARED_EVENT, cleared);
    window.addEventListener(ENTITY_CHANGED_EVENT, changed);
    window.addEventListener("storage", storage);
    return () => {
      window.removeEventListener(SESSION_UPDATED_EVENT, session);
      window.removeEventListener(SESSION_CLEARED_EVENT, cleared);
      window.removeEventListener(ENTITY_CHANGED_EVENT, changed);
      window.removeEventListener("storage", storage);
    };
  }, []);


  return (
    <QueryClientProvider key={sessionRevision} client={queryClient}>
      <I18nProvider locale="zh-CN">
        <HeroUIProvider navigate={navigate} useHref={useHref}>
          {children}
          <Toaster
            position="top-center"
            toastOptions={{
              duration: 2000,
              style: { ...toastSurfaceStyle },
              success: {
                duration: 2000,
                style: { ...toastSurfaceStyle },
              },
              error: {
                // P2-3：错误常含长 URL/堆栈，2 秒看不全且不折行会被截断；
                // 延长到 8 秒并允许长串断行、限制最大宽度。
                duration: 8000,
                style: {
                  ...toastSurfaceStyle,
                  maxWidth: "90vw",
                  wordBreak: "break-all",
                },
              },
            }}
          />
        </HeroUIProvider>
      </I18nProvider>
    </QueryClientProvider>
  );
}
