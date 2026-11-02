// #473: shared chart palette for the insights pages. Replaces the antd
// default-blue scale with the shell design-system accents (indigo primary,
// emerald/amber/red/violet series colors) — single source so charts stay
// consistent across Latency/Capacity/Dashboard/Anomalies/Compare.
export const CHART = {
    primary: "#6366f1",
    success: "#10b981",
    warning: "#f59e0b",
    danger: "#ef4444",
    violet: "#8b5cf6",
} as const;
