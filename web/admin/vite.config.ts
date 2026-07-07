import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.VITE_PROXY_TARGET || "http://127.0.0.1:6767";

  return {
    base: "/admin/",
    plugins: [react()],
    server: {
      proxy: {
        "/admin/backend-quotas": target,
        "/admin/cache": target,
        "/admin/catalog": target,
        "/admin/config": target,
        "/admin/client-models": target,
        "/admin/keys": target,
        "/admin/logs": target,
        "/admin/models": target,
        "/admin/nodes": target,
        "/admin/providers": target,
        "/admin/router-settings": target,
        "/admin/traces": target,
        "/v1": target
      }
    }
  };
});
