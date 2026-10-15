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
        port: 11431,
        proxy: {
            "/admin/api": {
                target: "http://localhost:11432",
                changeOrigin: true,
            },
        },
    },
});
