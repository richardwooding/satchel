import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";
import { fileURLToPath } from "node:url";

// gloam.css is vendored once, in web/src, and shared with the browser page.
const repoRoot = fileURLToPath(new URL("../..", import.meta.url));

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
    fs: { allow: [repoRoot] },
  },
  plugins: [react(), wails("./bindings")],
});
