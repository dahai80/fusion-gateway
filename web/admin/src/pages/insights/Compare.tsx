import { useState, useEffect } from "react";
import { Card, Spin, Select, Switch, Typography, Input, InputNumber, Row, Col, Alert, Table, Tag, Tooltip } from "antd";
import {
    BarChart,
    Bar,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip as RTooltip,
    ResponsiveContainer,
} from "recharts";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert, PERIOD_OPTIONS, windowFor } from "./shared";

const { Title } = Typography;

interface CompareRow {
    backend: string;
    backend_type: string;
    model: string;
    requests: number;
    success_rate: number;
    ttft_p50_ms: number;
    ttft_p95_ms: number;
    tpot_p50_ms: number;
    tpot_p95_ms: number;
    avg_tokens_per_sec: number;
    avg_input_tokens: number;
    avg_output_tokens: number;
    cost_total: number;
    diff_x: number;
}

interface BreakerTrip {
    backend: string;
    reason: string;
    count: number;
}

type DisplayRow = CompareRow & { label: string; diff_x: number };

interface CompareResponse {
    from: string;
    to: string;
    overlap: boolean;
    rows: CompareRow[];
    breaker_trips?: BreakerTrip[];
}

export default function Compare() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [data, setData] = useState<CompareResponse | null>(null);
    const [period, setPeriod] = useState(168);
    const [projectID, setProjectID] = useState("");
    const [overlap, setOverlap] = useState(false);
    const [inputMin, setInputMin] = useState<number | null>(null);
    const [inputMax, setInputMax] = useState<number | null>(null);

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        const params: Record<string, unknown> = windowFor(period);
        if (projectID) params.project_id = projectID;
        if (overlap) params.overlap = "true";
        if (inputMin) params.input_min = inputMin;
        if (inputMax) params.input_max = inputMax;
        client
            .get("/insights/compare", { params })
            .then((res) => {
                if (!cancelled) {
                    setData(res.data);
                    setUnavailable(false);
                }
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
    }, [period, projectID, overlap, inputMin, inputMax]);

    if (unavailable) return <UnavailableAlert />;

    const rawRows = data?.rows || [];
    // Diff auto-annotation (AC4): each row's TTFT p95 as a multiple of the
    // best (lowest) p95 in the window — the fastest row is the baseline.
    const bestP95 = Math.min(...rawRows.map((r) => r.ttft_p95_ms).filter((v) => v > 0));
    const rows: DisplayRow[] = rawRows.map((r) => ({
        ...r,
        label: `${r.backend}/${r.model}`,
        diff_x: bestP95 > 0 ? r.ttft_p95_ms / bestP95 : 0,
    }));
    const tripsByBackend = new Map<string, number>();
    for (const t of data?.breaker_trips || []) {
        tripsByBackend.set(t.backend, (tripsByBackend.get(t.backend) || 0) + t.count);
    }
    const tripsFor = (backend: string) => {
        let total = 0;
        for (const [b, c] of tripsByBackend) {
            if (backend === b || backend.startsWith(b + "/") || b.startsWith(backend)) total += c;
        }
        return total;
    };

    const numSorter = (key: keyof CompareRow) =>
        (a: DisplayRow, b: DisplayRow) => (a[key] as number) - (b[key] as number);

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Provider Comparison</Title>
                <Row gutter={8}>
                    <Col>
                        <Input
                            placeholder="project"
                            value={projectID}
                            onChange={(e) => setProjectID(e.target.value)}
                            style={{ width: 140 }}
                            allowClear
                        />
                    </Col>
                    <Col>
                        <Tooltip title="Fair comparison (AC5): keep only hourly buckets where every backend/model group has traffic — providers are compared over shared load, not each one's own peak hours.">
                            <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                                overlap <Switch size="small" checked={overlap} onChange={setOverlap} />
                            </span>
                        </Tooltip>
                    </Col>
                    <Col>
                        <Tooltip title="Prompt-length bucket (AC5): restrict to requests with input_tokens in [min, max]. Empty = unbounded.">
                            <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                                in <InputNumber size="small" min={0} value={inputMin || undefined} onChange={(v) => setInputMin(v ?? 0)} style={{ width: 80 }} placeholder="min" />
                                – <InputNumber size="small" min={0} value={inputMax || undefined} onChange={(v) => setInputMax(v ?? 0)} style={{ width: 80 }} placeholder="max" />
                            </span>
                        </Tooltip>
                    </Col>
                    <Col>
                        <Select
                            value={period}
                            onChange={setPeriod}
                            style={{ width: 120 }}
                            options={PERIOD_OPTIONS}
                        />
                    </Col>
                </Row>
            </div>
            {loading ? (
                <div style={{ textAlign: "center", padding: 80 }}>
                    <Spin size="large" />
                </div>
            ) : rows.length === 0 ? (
                <Alert type="info" showIcon message="No generation traffic in this window." />
            ) : (
                <>
                    <Card title="TTFT P95 by Backend / Model (ms)" style={{ marginBottom: 24 }}>
                        <ResponsiveContainer width="100%" height={320}>
                            <BarChart data={rows}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="label" angle={-20} textAnchor="end" height={70} />
                                <YAxis unit="ms" />
                                <RTooltip />
                                <Bar dataKey="ttft_p95_ms" name="TTFT p95" fill="#1677ff" />
                            </BarChart>
                        </ResponsiveContainer>
                    </Card>
                    <Card title="Detail">
                        <Table
                            dataSource={rows}
                            rowKey={(r) => `${r.backend}/${r.model}`}
                            pagination={{ pageSize: 20, showSizeChanger: false }}
                            columns={[
                                {
                                    title: "Backend",
                                    dataIndex: "backend",
                                    render: (v: string, r: DisplayRow) => (
                                        <span>
                                            {v}{" "}
                                            <Tag color={r.backend_type === "local" ? "green" : "blue"}>{r.backend_type || "cloud"}</Tag>
                                        </span>
                                    ),
                                },
                                { title: "Model", dataIndex: "model" },
                                { title: "Requests", dataIndex: "requests", sorter: numSorter("requests"), render: (v: number) => v.toLocaleString() },
                                { title: "Success %", dataIndex: "success_rate", sorter: numSorter("success_rate"), render: (v: number) => v.toFixed(2) },
                                { title: "TTFT p50", dataIndex: "ttft_p50_ms", sorter: numSorter("ttft_p50_ms"), render: (v: number) => `${Math.round(v)} ms` },
                                { title: "TTFT p95", dataIndex: "ttft_p95_ms", sorter: numSorter("ttft_p95_ms"), render: (v: number) => `${Math.round(v)} ms` },
                                {
                                    title: "Diff",
                                    dataIndex: "diff_x",
                                    width: 110,
                                    sorter: (a: DisplayRow, b: DisplayRow) => a.diff_x - b.diff_x,
                                    render: (v: number) =>
                                        v > 0 && v < 1.05 ? <Tag color="green">baseline</Tag> :
                                        v >= 2 ? <Tag color="red">{v.toFixed(1)}×</Tag> :
                                        v > 0 ? <Tag color="orange">{v.toFixed(1)}×</Tag> : "-",
                                },
                                { title: "TPOT p50", dataIndex: "tpot_p50_ms", sorter: numSorter("tpot_p50_ms"), render: (v: number) => `${v.toFixed(1)} ms` },
                                { title: "TPOT p95", dataIndex: "tpot_p95_ms", sorter: numSorter("tpot_p95_ms"), render: (v: number) => `${v.toFixed(1)} ms` },
                                { title: "Tok/s", dataIndex: "avg_tokens_per_sec", sorter: numSorter("avg_tokens_per_sec"), render: (v: number) => v.toFixed(1) },
                                { title: "Avg In", dataIndex: "avg_input_tokens", render: (v: number) => Math.round(v).toLocaleString() },
                                { title: "Avg Out", dataIndex: "avg_output_tokens", render: (v: number) => Math.round(v).toLocaleString() },
                                { title: "Cost", dataIndex: "cost_total", sorter: numSorter("cost_total"), render: (v: number) => v.toFixed(4) },
                                {
                                    title: "Breaker Trips",
                                    key: "breaker",
                                    width: 110,
                                    render: (_v: unknown, r: DisplayRow) => {
                                        const n = tripsFor(r.backend);
                                        return n > 0 ? <Tag color="red">{n}</Tag> : <Tag>0</Tag>;
                                    },
                                },
                            ]}
                        />
                    </Card>
                </>
            )}
        </div>
    );
}
