import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.VITE_PROXY_TARGET || "http://127.0.0.1:4000";

  return {
    base: "/admin/",
    plugins: [react()],
    server: {
      proxy: {
        "/admin/cache": target,
        "/admin/config": target,
        "/admin/client-models": target,
        "/admin/logs": target,
        "/admin/models": target,
        "/admin/nodes": target,
        "/admin/providers": target,
        "/admin/traces": target,
        "/v1": target
      }
    }
  };
});
