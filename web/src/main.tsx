import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { ErrorBoundary } from "./ErrorBoundary";
import { initializeTheme, ThemeProvider } from "./components/theme";
import "./index.css";

initializeTheme();

const root = document.getElementById("root");

if (!root) {
  throw new Error("Vastora root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <ErrorBoundary>
      <ThemeProvider><App /></ThemeProvider>
    </ErrorBoundary>
  </StrictMode>
);
