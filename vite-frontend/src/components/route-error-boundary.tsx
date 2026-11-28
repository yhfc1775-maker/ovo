import { Component, type ReactNode } from "react";

import { Button } from "@/shadcn-bridge/heroui/button";

// Suspense covers a pending import, but a rejected import (or a page render
// error) needs an error boundary. Keep the surrounding navigation usable.
export class RouteErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) {
      return (
        <div className="flex min-h-64 flex-col items-center justify-center gap-4 p-6 text-center" role="alert">
          <p className="font-semibold text-foreground">页面加载失败</p>
          <p className="text-sm text-foreground">请检查网络后重新加载，也可以切换到其他页面。</p>
          <Button color="primary" onPress={() => window.location.reload()}>
            重新加载页面
          </Button>
        </div>
      );
    }

    return this.props.children;
  }
}
