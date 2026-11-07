import { Alert } from "antd";

export const isUnavailable = (err: unknown): boolean =>
    (err as { response?: { status?: number } })?.response?.status === 503;

export const UnavailableAlert = () => (
    <Alert
        type="warning"
        showIcon
        message="Insights unavailable"
        description="Persistence is disabled or PostgreSQL is not configured. These dashboards read the persistence layer — enable persistence (persistence.enabled) and set the DSN env var, then restart the gateway."
    />
);

// #564: operator time presets (1h / 1d / 7d / 30d) — hours back from now.
export const PERIOD_OPTIONS = [
    { label: "1 Hour", value: 1 },
    { label: "24 Hours", value: 24 },
    { label: "7 Days", value: 168 },
    { label: "30 Days", value: 720 },
];

export const windowFor = (hours: number) => {
    const to = new Date();
    const from = new Date(to.getTime() - hours * 3600 * 1000);
    return { from: from.toISOString(), to: to.toISOString() };
};
