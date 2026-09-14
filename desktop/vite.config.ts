import { defineConfig } from "vitest/config";
import type { Plugin } from "vite";
import react from "@vitejs/plugin-react";
import http from "node:http";
import { existsSync } from "node:fs";
import path from "node:path";
import os from "node:os";

/** Where bnmd listens: BNM_SOCKET, then $XDG_RUNTIME_DIR/bnm/bnmd.sock, then /tmp/bnm-<uid>/bnmd.sock. */
function daemonSocket(): string {
  if (process.env.BNM_SOCKET) return process.env.BNM_SOCKET;
  const rt = process.env.XDG_RUNTIME_DIR;
  if (rt) {
    const p = path.join(rt, "bnm", "bnmd.sock");
    if (existsSync(p)) return p;
  }
  const uid = typeof os.userInfo === "function" ? os.userInfo().uid : 0;
  return path.join(os.tmpdir(), `bnm-${uid}`, "bnmd.sock");
}

/**
 * Dev-only proxy: /api/<path> -> bnmd over its Unix socket. Bodies and responses are
 * piped, never buffered, so the SSE event stream and the speed test stream flow
 * through unchanged. In the Tauri app the Rust shell owns the socket instead.
 */
function bnmdProxy(): Plugin {
  return {
    name: "bnmd-unix-socket-proxy",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!req.url || !req.url.startsWith("/api/")) return next();
        const socketPath = daemonSocket();
        const upstream = http.request(
          {
            socketPath,
            path: req.url.slice("/api".length),
            method: req.method,
            headers: { ...req.headers, host: "bnmd", connection: "keep-alive" },
          },
          (up) => {
            res.writeHead(up.statusCode ?? 502, { ...up.headers, "cache-control": "no-cache" });
            if (up.headers["content-type"]?.includes("text/event-stream")) res.flushHeaders?.();
            up.pipe(res);
          },
        );
        upstream.on("error", (err: NodeJS.ErrnoException) => {
          if (res.headersSent) return res.end();
          res.writeHead(503, { "content-type": "application/json" });
          res.end(JSON.stringify({ error: `daemon-unreachable: ${err.code ?? err.message} (${socketPath})`, code: "unavailable" }));
        });
        // The client went away (tab closed, SSE cancelled): drop the upstream too.
        res.on("close", () => upstream.destroy());
        req.pipe(upstream);
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), bnmdProxy()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  clearScreen: false,
  server: { port: 5173, strictPort: true, host: "127.0.0.1", watch: { ignored: ["**/src-tauri/**"] } },
  envPrefix: ["VITE_", "TAURI_ENV_"],
  build: {
    outDir: "dist",
    target: "es2023",
    sourcemap: false,
    // The sprite is 100 KB of SVG paths; one chunk is fine for a local app.
    chunkSizeWarningLimit: 900,
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
