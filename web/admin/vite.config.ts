import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
    plugins: [react()],
    base: "/admin/",
    build: {
        outDir: "../../internal/admin/ui/dist",
        emptyOutDir: true,
    },
    server: {
        proxy: {
            "/admin/api": {
                target: "http://localhost:8100",
                changeOrigin: true,
            },
        },
    },
});
