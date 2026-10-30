// #421: persistent (PG-backed) Dashboard — KPI totals, cache hit rates,
// latency percentiles, top models/backends and the request/token trend.
// Reads /admin/api/insights/overview; survives restarts (unlike the removed
// in-memory legacy Dashboard).
import { useState, useEffect } from "react";
import { Card, Spin, Select, Typography, Row, Col, Alert, Statistic, Table } from "antd";
import {
    ComposedChart,
    Line,
    Bar,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip as RTooltip,
    Legend,
    ResponsiveContainer,
} from "recharts";
import {
    CheckCircleOutlined,
    CloseCircleOutlined,
    ThunderboltOutlined,
    DollarOutlined,
    ClockCircleOutlined,
    DatabaseOutlined,
} from "@ant-design/icons";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert, PERIOD_OPTIONS, windowFor } from "./shared";

const { Title } = Typography;

interface OverviewTotals {
    requests: number;
    successes: number;
    errors: number;
    success_rate: number;
    input_tokens: number;
    output_tokens: number;
    cached_tokens: number;
    total_tokens: number;
    cost: number;
    local_savings: number;
}

interface OverviewLatency {
    duration_p50_ms: number;
    duration_p95_ms: number;
    duration_p99_ms: number;
    ttft_p50_ms: number;
    ttft_p95_ms: number;
    ttft_p99_ms: number;
}

interface OverviewCache {
    cache: string;
    hits: number;
    misses: number;
    hit_rate: number;
    size: number;
    samples: number;
}

interface OverviewModel {
    name: string;
    requests: number;
    total_tokens: number;
    cost: number;
}

interface OverviewBucket {
    bucket: string;
    requests: number;
    errors: number;
    input_tokens: number;
    output_tokens: number;
    cached_tokens: number;
}

interface OverviewResponse {
    from: string;
    to: string;
    granularity: string;
    totals: OverviewTotals;
    latency: OverviewLatency;
    gateway_caches: OverviewCache[] | null;
    top_models: OverviewModel[] | null;
    top_backends: OverviewModel[] | null;
    series: OverviewBucket[] | null;
}

const granularityFor = (hours: number) => (hours <= 24 ? "hour" : "day");

const fmtTokens = (n: number) => {
    if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`;
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
    return `${n}`;
};

// Upstream prompt-cache hit rate: cached input tokens over all input tokens.
const promptCacheRate = (t: OverviewTotals | undefined) =>
    t && t.input_tokens > 0 ? t.cached_tokens / t.input_tokens : 0;

export default function Dashboard() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [data, setData] = useState<OverviewResponse | null>(null);
    const [period, setPeriod] = useState(24);

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        client
            .get("/insights/overview", {
                params: { ...windowFor(period), granularity: granularityFor(period) },
            })
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
    }, [period]);

    if (unavailable) return <UnavailableAlert />;

    const totals = data?.totals;
    const latency = data?.latency;
    const series = data?.series || [];
    const caches = data?.gateway_caches || [];
    const topColumns = [
        { title: "Name", dataIndex: "name", key: "name", ellipsis: true },
        { title: "Requests", dataIndex: "requests", key: "requests", sorter: (a: OverviewModel, b: OverviewModel) => a.requests - b.requests, render: (v: number) => v.toLocaleString() },
        { title: "Tokens", dataIndex: "total_tokens", key: "total_tokens", render: (v: number) => fmtTokens(v) },
        { title: "Cost", dataIndex: "cost", key: "cost", render: (v: number) => `$${v.toFixed(4)}` },
    ];

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16, gap: 8 }}>
                <Title level={4} style={{ margin: 0 }}>Dashboard</Title>
                <Select value={period} onChange={setPeriod} style={{ width: 120 }} options={PERIOD_OPTIONS} />
            </div>
            {loading ? (
                <div style={{ textAlign: "center", padding: 80 }}>
                    <Spin size="large" />
                </div>
            ) : (
                <>
                    <Row gutter={[16, 16]} style={{ marginBottom: 24 }}>
                        <Col xs={12} md={6} xl={4}>
                            <Card size="small">
                                <Statistic title="Requests" value={(totals?.requests || 0).toLocaleString()} prefix={<ThunderboltOutlined />} />
                                <div style={{ marginTop: 8, fontSize: 13 }}>
                                    <span style={{ color: "#389e0d" }}><CheckCircleOutlined /> {totals?.successes || 0}</span>
                                    {"  "}
                                    <span style={{ color: "#cf1322" }}><CloseCircleOutlined /> {totals?.errors || 0}</span>
                                    {"  "}
                                    <span>success {((totals?.success_rate || 0) * 100).toFixed(1)}%</span>
                                </div>
                            </Card>
                        </Col>
                        <Col xs={12} md={6} xl={4}>
                            <Card size="small">
                                <Statistic title="Total Tokens" value={fmtTokens(totals?.total_tokens || 0)} />
                                <div style={{ marginTop: 8, fontSize: 13 }}>
                                    <span>in {fmtTokens(totals?.input_tokens || 0)}</span>
                                    {"  "}
                                    <span>out {fmtTokens(totals?.output_tokens || 0)}</span>
                                    {"  "}
                                    <span>cached {fmtTokens(totals?.cached_tokens || 0)}</span>
                                </div>
                            </Card>
                        </Col>
                        <Col xs={12} md={6} xl={4}>
                            <Card size="small">
                                <Statistic title="Prompt Cache Hit" value={((promptCacheRate(totals) * 100).toFixed(1))} suffix="%" prefix={<DatabaseOutlined />} />
                                <div style={{ marginTop: 8, fontSize: 13 }}>
                                    {caches.map((c) => (
                                        <div key={c.cache}>
                                            {c.cache}: {(c.hit_rate * 100).toFixed(1)}% ({c.hits.toLocaleString()}/{(c.hits + c.misses).toLocaleString()})
                                        </div>
                                    ))}
                                    {caches.length === 0 ? <span>no gateway cache samples</span> : null}
                                </div>
                            </Card>
                        </Col>
                        <Col xs={12} md={6} xl={4}>
                            <Card size="small">
                                <Statistic title="Cost (USD)" value={totals?.cost || 0} precision={4} prefix={<DollarOutlined />} />
                                <div style={{ marginTop: 8, fontSize: 13 }}>
                                    <span>local savings ${(totals?.local_savings || 0).toFixed(4)}</span>
                                </div>
                            </Card>
                        </Col>
                        <Col xs={12} md={6} xl={4}>
                            <Card size="small">
                                <Statistic title="Duration p95" value={Math.round(latency?.duration_p95_ms || 0)} suffix="ms" prefix={<ClockCircleOutlined />} />
                                <div style={{ marginTop: 8, fontSize: 13 }}>
                                    <span>p50 {Math.round(latency?.duration_p50_ms || 0)}ms</span>
                                    {"  "}
                                    <span>p99 {Math.round(latency?.duration_p99_ms || 0)}ms</span>
                                </div>
                            </Card>
                        </Col>
                        <Col xs={12} md={6} xl={4}>
                            <Card size="small">
                                <Statistic title="TTFT p95" value={Math.round(latency?.ttft_p95_ms || 0)} suffix="ms" prefix={<ClockCircleOutlined />} />
                                <div style={{ marginTop: 8, fontSize: 13 }}>
                                    <span>p50 {Math.round(latency?.ttft_p50_ms || 0)}ms</span>
                                    {"  "}
                                    <span>p99 {Math.round(latency?.ttft_p99_ms || 0)}ms</span>
                                </div>
                            </Card>
                        </Col>
                    </Row>
                    <Card title="Requests & Tokens Trend" style={{ marginBottom: 24 }}>
                        {series.length === 0 ? (
                            <Alert type="info" showIcon message="No traffic in this window." />
                        ) : (
                            <ResponsiveContainer width="100%" height={320}>
                                <ComposedChart data={series}>
                                    <CartesianGrid strokeDasharray="3 3" />
                                    <XAxis dataKey="bucket" />
                                    <YAxis yAxisId="left" />
                                    <YAxis yAxisId="right" orientation="right" />
                                    <RTooltip />
                                    <Legend />
                                    <Bar yAxisId="right" dataKey="requests" name="requests" fill="#1677ff" fillOpacity={0.15} />
                                    <Bar yAxisId="right" dataKey="errors" name="errors" fill="#faad14" fillOpacity={0.2} />
                                    <Line yAxisId="left" type="monotone" dataKey="input_tokens" name="input tokens" stroke="#1677ff" strokeWidth={2} dot={false} />
                                    <Line yAxisId="left" type="monotone" dataKey="output_tokens" name="output tokens" stroke="#52c41a" strokeWidth={2} dot={false} />
                                    <Line yAxisId="left" type="monotone" dataKey="cached_tokens" name="cached tokens" stroke="#722ed1" strokeWidth={1.5} strokeDasharray="6 3" dot={false} />
                                </ComposedChart>
                            </ResponsiveContainer>
                        )}
                    </Card>
                    <Row gutter={[16, 16]}>
                        <Col xs={24} xl={12}>
                            <Card title="Top Models" size="small">
                                <Table size="small" rowKey="name" pagination={false} columns={topColumns} dataSource={data?.top_models || []} />
                            </Card>
                        </Col>
                        <Col xs={24} xl={12}>
                            <Card title="Top Backends" size="small">
                                <Table size="small" rowKey="name" pagination={false} columns={topColumns} dataSource={data?.top_backends || []} />
                            </Card>
                        </Col>
                    </Row>
                </>
            )}
        </div>
    );
}
