import { Component, type ErrorInfo, type ReactNode } from "react";

type ErrorBoundaryState = { failed: boolean };

export class ErrorBoundary extends Component<{ children: ReactNode }, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Vastora interface failed", error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    const chinese = document.documentElement.lang === "zh-CN";
    return (
      <main className="grid min-h-svh place-items-center bg-muted/30 p-6">
        <section className="w-full max-w-sm rounded-2xl border bg-card p-6 shadow-sm">
          <h1 className="text-lg font-semibold">{chinese ? "页面暂时无法显示" : "This page could not be displayed"}</h1>
          <p className="mt-2 text-sm leading-6 text-muted-foreground">
            {chinese ? "你的设置没有丢失。重新载入页面即可继续。" : "Your settings are safe. Reload the page to continue."}
          </p>
          <button className="mt-5 min-h-11 rounded-xl bg-primary px-4 text-sm font-medium text-primary-foreground" onClick={() => window.location.reload()} type="button">
            {chinese ? "重新载入" : "Reload"}
          </button>
        </section>
      </main>
    );
  }
}
