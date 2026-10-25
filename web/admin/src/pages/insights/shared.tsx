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

export const PERIOD_OPTIONS = [
    { label: "24 Hours", value: 24 },
    { label: "3 Days", value: 72 },
    { label: "7 Days", value: 168 },
    { label: "30 Days", value: 720 },
];

export const windowFor = (hours: number) => {
    const to = new Date();
    const from = new Date(to.getTime() - hours * 3600 * 1000);
    return { from: from.toISOString(), to: to.toISOString() };
};
