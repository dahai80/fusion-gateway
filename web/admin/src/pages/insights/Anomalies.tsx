import { useState, useEffect, useCallback } from "react";
import { Card, Typography, Select, Input, Table, Tag, Button, Popconfirm, Modal, InputNumber, message, Spin, Alert } from "antd";
import {
    ComposedChart,
    Bar,
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

interface Anomaly {
    id: number;
    ts: string;
    detected_at: string;
    kind: string;
    backend?: string;
    model?: string;
    severity: string;
    evidence: Record<string, unknown>;
    status: string;
    acked_by?: string;
    muted_until?: string;
}

const severityColor = (s: string) =>
    s === "high" ? "red" : s === "warn" ? "orange" : "blue";

interface TimelineBucket {
    bucket: string;
    requests: number;
    errors: number;
    breaker_events: number;
    sys_mem_ratio: number;
    mlx_mem_ratio: number;
}

export default function Anomalies() {
    const [messageApi, messageCtx] = message.useMessage();
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [rows, setRows] = useState<Anomaly[]>([]);
    const [status, setStatus] = useState("");
    const [kind, setKind] = useState("");
    const [muteTarget, setMuteTarget] = useState<Anomaly | null>(null);
    const [muteDays, setMuteDays] = useState(7);
    const [timelinePeriod, setTimelinePeriod] = useState(24);
    const [timeline, setTimeline] = useState<TimelineBucket[]>([]);
    const [timelineLoading, setTimelineLoading] = useState(true);

    useEffect(() => {
        let cancelled = false;
        setTimelineLoading(true);
        const granularity = timelinePeriod > 72 ? "day" : "hour";
        client.get("/insights/timeline", {
            params: { ...windowFor(timelinePeriod), granularity },
        })
            .then((res) => {
                if (!cancelled) setTimeline((res.data.series || []) as TimelineBucket[]);
            })
            .catch(() => {
                if (!cancelled) setTimeline([]);
            })
            .finally(() => {
                if (!cancelled) setTimelineLoading(false);
            });
        return () => {
            cancelled = true;
        };
    }, [timelinePeriod]);

    const fetchAnomalies = useCallback(async () => {
        setLoading(true);
        const params: Record<string, unknown> = { limit: 200 };
        if (status) params.status = status;
        if (kind) params.kind = kind;
        try {
            const res = await client.get("/anomalies", { params });
            setRows(res.data.anomalies || []);
            setUnavailable(false);
        } catch (err) {
            setUnavailable(isUnavailable(err));
        } finally {
            setLoading(false);
        }
    }, [status, kind]);

    useEffect(() => {
        fetchAnomalies();
    }, [fetchAnomalies]);

    const ackAnomaly = async (a: Anomaly) => {
        try {
            await client.post(`/anomalies/${a.id}/ack`, {});
            messageApi.success(`Anomaly #${a.id} acked`);
            fetchAnomalies();
        } catch {
            messageApi.error("ack failed");
        }
    };

    const muteAnomaly = async () => {
        if (!muteTarget) return;
        try {
            await client.post(`/anomalies/${muteTarget.id}/mute`, { days: muteDays });
            messageApi.success(`Anomaly #${muteTarget.id} muted for ${muteDays}d`);
            setMuteTarget(null);
            fetchAnomalies();
        } catch {
            messageApi.error("mute failed");
        }
    };

    if (unavailable) return <UnavailableAlert />;

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Log Anomalies</Title>
                <div style={{ display: "flex", gap: 8 }}>
                    <Input
                        placeholder="kind"
                        value={kind}
                        onChange={(e) => setKind(e.target.value)}
                        style={{ width: 180 }}
                        allowClear
                    />
                    <Select
                        value={status}
                        onChange={setStatus}
                        style={{ width: 140 }}
                        options={[
                            { label: "Open", value: "open" },
                            { label: "Acked", value: "acked" },
                            { label: "Muted", value: "muted" },
                            { label: "All", value: "" },
                        ]}
                    />
                </div>
            </div>
            <Card
                title="Timeline — requests / errors / breaker events / memory"
                extra={
                    <Select
                        value={timelinePeriod}
                        onChange={setTimelinePeriod}
                        style={{ width: 120 }}
                        options={PERIOD_OPTIONS}
                    />
                }
                style={{ marginBottom: 24 }}
            >
                {timelineLoading ? (
                    <div style={{ textAlign: "center", padding: 60 }}>
                        <Spin size="large" />
                    </div>
                ) : timeline.length === 0 ? (
                    <Alert
                        type="info"
                        showIcon
                        message="No traffic, events, or hardware samples in this window."
                    />
                ) : (
                    <ResponsiveContainer width="100%" height={320}>
                        <ComposedChart
                            data={timeline.map((b) => ({
                                ...b,
                                time: b.bucket.replace("T", " ").slice(5, 16),
                                sys_mem_pct: Math.round(b.sys_mem_ratio * 1000) / 10,
                                mlx_mem_pct: Math.round(b.mlx_mem_ratio * 1000) / 10,
                            }))}
                        >
                            <CartesianGrid strokeDasharray="3 3" />
                            <XAxis dataKey="time" minTickGap={60} />
                            <YAxis yAxisId="traffic" allowDecimals={false} />
                            <YAxis yAxisId="mem" orientation="right" unit="%" domain={[0, 100]} />
                            <RTooltip />
                            <Legend />
                            <Bar yAxisId="traffic" dataKey="requests" name="requests" fill="#1677ff" />
                            <Bar yAxisId="traffic" dataKey="errors" name="errors" fill="#ff4d4f" />
                            <Bar yAxisId="traffic" dataKey="breaker_events" name="breaker events" fill="#faad14" />
                            <Line yAxisId="mem" type="monotone" dataKey="sys_mem_pct" name="system mem %" stroke="#722ed1" strokeWidth={1.5} dot={false} />
                            <Line yAxisId="mem" type="monotone" dataKey="mlx_mem_pct" name="MLX mem %" stroke="#13c2c2" strokeWidth={1.5} dot={false} />
                        </ComposedChart>
                    </ResponsiveContainer>
                )}
            </Card>
            <Card>
                <Table
                    dataSource={rows}
                    rowKey="id"
                    loading={loading}
                    pagination={{ pageSize: 20, showSizeChanger: false }}
                    columns={[
                        { title: "ID", dataIndex: "id", width: 60 },
                        { title: "Detected", dataIndex: "detected_at", width: 180, render: (v: string) => v.replace("T", " ").slice(0, 19) },
                        { title: "Kind", dataIndex: "kind", width: 200 },
                        { title: "Backend", dataIndex: "backend", width: 120, render: (v?: string) => v || "-" },
                        { title: "Model", dataIndex: "model", ellipsis: true, render: (v?: string) => v || "-" },
                        {
                            title: "Severity",
                            dataIndex: "severity",
                            width: 100,
                            render: (v: string) => <Tag color={severityColor(v)}>{v}</Tag>,
                        },
                        {
                            title: "Evidence",
                            dataIndex: "evidence",
                            render: (v: Anomaly["evidence"]) => <code style={{ fontSize: 12 }}>{JSON.stringify(v)}</code>,
                        },
                        {
                            title: "Status",
                            dataIndex: "status",
                            width: 100,
                            render: (v: string, a: Anomaly) => (
                                <span>
                                    <Tag color={v === "open" ? "orange" : v === "acked" ? "green" : "default"}>{v}</Tag>
                                    {a.acked_by ? <span style={{ fontSize: 12 }}>by {a.acked_by}</span> : null}
                                </span>
                            ),
                        },
                        {
                            title: "Actions",
                            width: 160,
                            render: (_v: unknown, a: Anomaly) =>
                                a.status === "open" ? (
                                    <>
                                        <Popconfirm title="Acknowledge this anomaly?" onConfirm={() => ackAnomaly(a)}>
                                            <Button type="link" size="small">Ack</Button>
                                        </Popconfirm>
                                        <Button type="link" size="small" onClick={() => setMuteTarget(a)}>
                                            Mute
                                        </Button>
                                    </>
                                ) : a.status === "acked" ? (
                                    <Button type="link" size="small" onClick={() => setMuteTarget(a)}>
                                        Mute
                                    </Button>
                                ) : (
                                    <span style={{ fontSize: 12 }}>{a.muted_until ? `until ${a.muted_until.slice(0, 10)}` : "-"}</span>
                                ),
                        },
                    ]}
                />
            </Card>
            <Modal
                open={!!muteTarget}
                title={`Mute anomaly #${muteTarget?.id}`}
                onOk={muteAnomaly}
                onCancel={() => setMuteTarget(null)}
            >
                <p>Mute this anomaly signature for how many days? (1–90, default 7)</p>
                <InputNumber min={1} max={90} value={muteDays} onChange={(v) => setMuteDays(v || 7)} />
            </Modal>
            {messageCtx}
        </div>
    );
}
