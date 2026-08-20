import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { ErrorBoundary } from "./ErrorBoundary";
import "./index.css";

const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");
const applySystemTheme = () => {
  document.documentElement.classList.toggle("dark", systemTheme.matches);
  document.documentElement.style.colorScheme = systemTheme.matches ? "dark" : "light";
};

applySystemTheme();
systemTheme.addEventListener("change", applySystemTheme);

const root = document.getElementById("root");

if (!root) {
  throw new Error("Vastora root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>
);
