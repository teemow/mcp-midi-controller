import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./App";
import { McpProvider } from "./mcp/McpProvider";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <McpProvider>
      <App />
    </McpProvider>
  </StrictMode>,
);
