import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  build: {
    emptyOutDir: false,
    outDir: "../web/static",
    lib: {
      entry: "src/main.tsx",
      formats: ["es"],
      fileName: () => "gamarr-react.js",
      cssFileName: "gamarr-react",
    },
    rollupOptions: {
      output: { assetFileNames: "[name][extname]" },
    },
  },
});
