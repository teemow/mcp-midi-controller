import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The SPA is served by the Go daemon under /app/ on the same loopback listener
// as the MCP endpoint, so every asset URL must live below /app/. The build is
// emitted straight into the Go embed dir (internal/webui/dist) which is
// committed so `go install` works from a clean checkout.
export default defineConfig({
  base: "/app/",
  plugins: [react()],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
  },
});
