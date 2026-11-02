// #473: dark-mode preference source for the admin console iframe. The shell
// host may push its theme via postMessage (source "fusion-shell", type
// "theme", payload {theme:"dark"|"light"}); when no host message has arrived
// we fall back to prefers-color-scheme so the console still flips with the OS.
import { useEffect, useState } from "react";

export function useShellTheme(): boolean {
    const [dark, setDark] = useState(
        () => window.matchMedia("(prefers-color-scheme: dark)").matches
    );
    useEffect(() => {
        const mq = window.matchMedia("(prefers-color-scheme: dark)");
        const onMq = (e: MediaQueryListEvent) => setDark(e.matches);
        mq.addEventListener("change", onMq);
        const onMsg = (e: MessageEvent) => {
            const d = e.data as { source?: string; type?: string; theme?: string } | null;
            if (!d || d.source !== "fusion-shell" || d.type !== "theme") return;
            if (d.theme === "dark" || d.theme === "light") {
                setDark(d.theme === "dark");
            }
        };
        window.addEventListener("message", onMsg);
        // Ask the host once on mount; shells that answer send the same
        // {source:"fusion-shell",type:"theme",theme:...} payload back.
        try {
            window.parent?.postMessage(
                { source: "fusion-admin-console", type: "theme-request" },
                "*"
            );
        } catch {
            // cross-origin parent — matchMedia fallback already covers it
        }
        return () => {
            mq.removeEventListener("change", onMq);
            window.removeEventListener("message", onMsg);
        };
    }, []);
    return dark;
}
