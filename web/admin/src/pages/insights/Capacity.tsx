import { useState, useEffect } from "react";
import { Card, Spin, Select, Typography, Alert, Tooltip } from "antd";
import {
    ComposedChart,
    Line,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip as RTooltip,
    Legend,
    ResponsiveContainer,
} from "recharts";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert, PERIOD_OPTIONS, windowFor } from "./shared";

const { Title } = Typography;

interface LatencyBucket {
    bucket: string;
    requests: number;
}

interface HWSample {
    ts: string;
    sys_mem_ratio: number;
    mlx_mem_ratio: number;
    swap_page_rate: number;
    gpu_util: number;
    loaded_models: number;
    collection_error: boolean;
}

// DAY_SLOTS / HOUR_SLOTS build the 24h×7d request heatmap grid (plan §14.1):
// one cell per hour of the last 7 days, colored by request volume.
const DAYS = 7;
const HOURS = 24;

const heatColor = (v: number, max: number): string => {
    if (max <= 0 || v <= 0) return "#f0f0f0";
    const t = Math.min(1, Math.sqrt(v / max));
    // white → indigo ramp, light (dark-mode-safe enough at low alpha values)
    const alpha = 0.15 + 0.85 * t;
    return `rgba(59, 76, 222, ${alpha.toFixed(2)})`;
};

export default function Capacity() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [period, setPeriod] = useState(168);
    const [buckets, setBuckets] = useState<LatencyBucket[]>([]);
    const [samples, setSamples] = useState<HWSample[]>([]);

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        const hours = Math.min(period, DAYS * HOURS);
        Promise.all([
            client.get("/insights/latency", {
                params: { ...windowFor(hours), granularity: "hour" },
            }),
            client.get("/insights/hw-samples", {
                params: { ...windowFor(period), limit: 10000 },
            }),
        ])
            .then(([lat, hw]) => {
                if (cancelled) return;
                setBuckets((lat.data.series || []) as LatencyBucket[]);
                setSamples((hw.data.samples || hw.data || []) as HWSample[]);
                setUnavailable(false);
            })
            .catch((err) => {
                if (!cancelled) setUnavailable(isUnavailable(err));
            })
            .finally(() => {
                if (!cancelled) setLoading(false);
            });
        return () => {
            cancelled = true;
        };
    }, [period]);

    if (unavailable) return <UnavailableAlert />;

    // Pivot the hourly buckets into a 24×7 grid: rows are the last 7 days,
    // columns are hour-of-day. Cells outside the selected window stay 0.
    const counts = new Map<string, number>();
    for (const b of buckets) {
        const at = new Date(b.bucket);
        const day = at.toISOString().slice(0, 10);
        counts.set(`${day}T${at.getHours()}`, b.requests);
    }
    const days: string[] = [];
    for (let d = DAYS - 1; d >= 0; d--) {
        days.push(new Date(Date.now() - d * 24 * 3600 * 1000).toISOString().slice(0, 10));
    }
    const maxCell = Math.max(0, ...Array.from(counts.values()));

    const hwData = samples.map((s) => ({
        ...s,
        time: s.ts.replace("T", " ").slice(5, 16),
        sys_mem_pct: Math.round(s.sys_mem_ratio * 1000) / 10,
        mlx_mem_pct: Math.round(s.mlx_mem_ratio * 1000) / 10,
        gpu_util_pct: Math.round(s.gpu_util * 1000) / 10,
    }));

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Capacity</Title>
                <Select
                    value={period}
                    onChange={setPeriod}
                    style={{ width: 120 }}
                    options={PERIOD_OPTIONS}
                />
            </div>
            {loading ? (
                <div style={{ textAlign: "center", padding: 80 }}>
                    <Spin size="large" />
                </div>
            ) : (
                <>
                    <Card title="Request Volume Heatmap (24h × 7d)" style={{ marginBottom: 24 }}>
                        <div style={{ overflowX: "auto" }}>
                            <table style={{ borderCollapse: "collapse", fontSize: 11 }}>
                                <thead>
                                    <tr>
                                        <th style={{ padding: "2px 8px", textAlign: "right" }}>day</th>
                                        {Array.from({ length: HOURS }, (_, h) => (
                                            <th key={h} style={{ padding: 2, fontWeight: h % 6 === 0 ? 600 : 400 }}>{h}</th>
                                        ))}
                                    </tr>
                                </thead>
                                <tbody>
                                    {days.map((day) => (
                                        <tr key={day}>
                                            <td style={{ padding: "2px 8px", whiteSpace: "nowrap" }}>{day.slice(5)}</td>
                                            {Array.from({ length: HOURS }, (_, h) => {
                                                const v = counts.get(`${day}T${h}`) || 0;
                                                return (
                                                    <td key={h} style={{ padding: 1 }}>
                                                        <Tooltip title={`${day} ${String(h).padStart(2, "0")}:00 — ${v} requests`}>
                                                            <div style={{ width: 18, height: 18, borderRadius: 3, background: heatColor(v, maxCell) }} />
                                                        </Tooltip>
                                                    </td>
                                                );
                                            })}
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </div>
                    </Card>
                    <Card title="Hardware Capacity (memory / GPU / loaded models)">
                        {hwData.length === 0 ? (
                            <Alert type="info" showIcon message="No hardware samples in this window (fg_hw_samples, 30s cadence)." />
                        ) : (
                            <ResponsiveContainer width="100%" height={340}>
                                <ComposedChart data={hwData}>
                                    <CartesianGrid strokeDasharray="3 3" />
                                    <XAxis dataKey="time" minTickGap={60} />
                                    <YAxis yAxisId="left" unit="%" domain={[0, 100]} />
                                    <YAxis yAxisId="right" orientation="right" allowDecimals={false} />
                                    <RTooltip />
                                    <Legend />
                                    <Line yAxisId="left" type="monotone" dataKey="sys_mem_pct" name="system mem %" stroke="#1677ff" strokeWidth={1.5} dot={false} />
                                    <Line yAxisId="left" type="monotone" dataKey="mlx_mem_pct" name="MLX mem %" stroke="#722ed1" strokeWidth={1.5} dot={false} />
                                    <Line yAxisId="left" type="monotone" dataKey="gpu_util_pct" name="GPU util %" stroke="#52c41a" strokeWidth={1.5} dot={false} />
                                    <Line yAxisId="right" type="stepAfter" dataKey="loaded_models" name="loaded models" stroke="#faad14" strokeWidth={1.5} dot={false} />
                                </ComposedChart>
                            </ResponsiveContainer>
                        )}
                    </Card>
                </>
            )}
        </div>
    );
}
