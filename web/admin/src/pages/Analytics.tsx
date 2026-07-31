import { useState, useEffect } from "react";
import { Tabs, Card, Spin, Row, Col, Statistic, Select, DatePicker, Typography } from "antd";
import {
    LineChart,
    Line,
    BarChart,
    Bar,
    PieChart,
    Pie,
    Cell,
    AreaChart,
    Area,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip,
    ResponsiveContainer,
    Legend,
} from "recharts";
import client from "../api/client";

const { Title } = Typography;
const { RangePicker } = DatePicker;

const COLORS = ["#1677ff", "#52c41a", "#faad14", "#ff4d4f", "#722ed1", "#13c2c2", "#eb2f96", "#fa541c"];

interface AnalyticsData {
    token: {
        summary: { total_prompt: number; total_completion: number; total_tokens: number; avg_per_request: number };
        trend: { date: string; prompt: number; completion: number }[];
    };
    cost: {
        summary: { total_cost: number; avg_per_request: number; local_cost: number; cloud_cost: number };
        trend: { date: string; local: number; cloud: number; total: number }[];
    };
    model: {
        distribution: { name: string; value: number }[];
        trend: { date: string; [model: string]: string | number }[];
    };
    latency: {
        summary: { avg_ms: number; p50_ms: number; p95_ms: number; p99_ms: number };
        trend: { date: string; avg: number; p95: number; p99: number }[];
    };
    error: {
        summary: { total_errors: number; error_rate: number; top_codes: { code: number; count: number }[] };
        trend: { date: string; errors: number; total: number }[];
    };
}

export default function Analytics() {
    const [loading, setLoading] = useState(true);
    const [data, setData] = useState<AnalyticsData | null>(null);
    const [period, setPeriod] = useState("7d");

    const fetchAnalytics = async () => {
        setLoading(true);
        try {
            const res = await client.get("/analytics", { params: { period } });
            setData(res.data?.data || res.data || null);
        } catch (err) {
            console.error("Failed to fetch analytics:", err);
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        fetchAnalytics();
    }, [period]);

    if (loading || !data) {
        return (
            <div style={{ textAlign: "center", padding: 80 }}>
                <Spin size="large" />
            </div>
        );
    }

    const tokenTab = (
        <div>
            <Row gutter={16} style={{ marginBottom: 24 }}>
                <Col span={6}>
                    <Card><Statistic title="Total Prompt Tokens" value={data.token.summary.total_prompt} /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="Total Completion Tokens" value={data.token.summary.total_completion} /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="Total Tokens" value={data.token.summary.total_tokens} /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="Avg Per Request" value={data.token.summary.avg_per_request} precision={0} /></Card>
                </Col>
            </Row>
            <Card title="Token Usage Trend">
                <ResponsiveContainer width="100%" height={360}>
                    <AreaChart data={data.token.trend}>
                        <CartesianGrid strokeDasharray="3 3" />
                        <XAxis dataKey="date" />
                        <YAxis />
                        <Tooltip />
                        <Legend />
                        <Area type="monotone" dataKey="prompt" stackId="1" stroke="#1677ff" fill="#1677ff" fillOpacity={0.3} />
                        <Area type="monotone" dataKey="completion" stackId="1" stroke="#52c41a" fill="#52c41a" fillOpacity={0.3} />
                    </AreaChart>
                </ResponsiveContainer>
            </Card>
        </div>
    );

    const costTab = (
        <div>
            <Row gutter={16} style={{ marginBottom: 24 }}>
                <Col span={6}>
                    <Card><Statistic title="Total Cost" value={data.cost.summary.total_cost} precision={4} prefix="$" /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="Avg Per Request" value={data.cost.summary.avg_per_request} precision={6} prefix="$" /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="Local Cost" value={data.cost.summary.local_cost} precision={4} prefix="$" valueStyle={{ color: "#52c41a" }} /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="Cloud Cost" value={data.cost.summary.cloud_cost} precision={4} prefix="$" valueStyle={{ color: "#faad14" }} /></Card>
                </Col>
            </Row>
            <Card title="Cost Trend">
                <ResponsiveContainer width="100%" height={360}>
                    <LineChart data={data.cost.trend}>
                        <CartesianGrid strokeDasharray="3 3" />
                        <XAxis dataKey="date" />
                        <YAxis />
                        <Tooltip />
                        <Legend />
                        <Line type="monotone" dataKey="local" stroke="#52c41a" strokeWidth={2} />
                        <Line type="monotone" dataKey="cloud" stroke="#faad14" strokeWidth={2} />
                        <Line type="monotone" dataKey="total" stroke="#1677ff" strokeWidth={2} />
                    </LineChart>
                </ResponsiveContainer>
            </Card>
        </div>
    );

    const modelTab = (
        <div>
            <Row gutter={16}>
                <Col span={12}>
                    <Card title="Model Distribution">
                        <ResponsiveContainer width="100%" height={360}>
                            <PieChart>
                                <Pie
                                    data={data.model.distribution}
                                    cx="50%"
                                    cy="50%"
                                    innerRadius={60}
                                    outerRadius={120}
                                    paddingAngle={2}
                                    dataKey="value"
                                    label={({ name, percent }) => `${name} ${(percent * 100).toFixed(0)}%`}
                                >
                                    {data.model.distribution.map((_entry, index) => (
                                        <Cell key={`cell-${index}`} fill={COLORS[index % COLORS.length]} />
                                    ))}
                                </Pie>
                                <Tooltip />
                                <Legend />
                            </PieChart>
                        </ResponsiveContainer>
                    </Card>
                </Col>
                <Col span={12}>
                    <Card title="Model Usage Trend">
                        <ResponsiveContainer width="100%" height={360}>
                            <BarChart data={data.model.trend}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="date" />
                                <YAxis />
                                <Tooltip />
                                <Legend />
                                {data.model.distribution.map((m, i) => (
                                    <Bar key={m.name} dataKey={m.name} stackId="a" fill={COLORS[i % COLORS.length]} />
                                ))}
                            </BarChart>
                        </ResponsiveContainer>
                    </Card>
                </Col>
            </Row>
        </div>
    );

    const latencyTab = (
        <div>
            <Row gutter={16} style={{ marginBottom: 24 }}>
                <Col span={6}>
                    <Card><Statistic title="Avg Latency" value={data.latency.summary.avg_ms} suffix="ms" /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="P50" value={data.latency.summary.p50_ms} suffix="ms" /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="P95" value={data.latency.summary.p95_ms} suffix="ms" valueStyle={{ color: "#faad14" }} /></Card>
                </Col>
                <Col span={6}>
                    <Card><Statistic title="P99" value={data.latency.summary.p99_ms} suffix="ms" valueStyle={{ color: "#ff4d4f" }} /></Card>
                </Col>
            </Row>
            <Card title="Latency Trend">
                <ResponsiveContainer width="100%" height={360}>
                    <LineChart data={data.latency.trend}>
                        <CartesianGrid strokeDasharray="3 3" />
                        <XAxis dataKey="date" />
                        <YAxis unit="ms" />
                        <Tooltip />
                        <Legend />
                        <Line type="monotone" dataKey="avg" stroke="#1677ff" strokeWidth={2} />
                        <Line type="monotone" dataKey="p95" stroke="#faad14" strokeWidth={2} strokeDasharray="5 5" />
                        <Line type="monotone" dataKey="p99" stroke="#ff4d4f" strokeWidth={2} strokeDasharray="5 5" />
                    </LineChart>
                </ResponsiveContainer>
            </Card>
        </div>
    );

    const errorTab = (
        <div>
            <Row gutter={16} style={{ marginBottom: 24 }}>
                <Col span={12}>
                    <Card><Statistic title="Total Errors" value={data.error.summary.total_errors} valueStyle={{ color: "#ff4d4f" }} /></Card>
                </Col>
                <Col span={12}>
                    <Card><Statistic title="Error Rate" value={data.error.summary.error_rate} suffix="%" precision={2} valueStyle={{ color: "#ff4d4f" }} /></Card>
                </Col>
            </Row>
            <Card title="Error Trend" style={{ marginBottom: 24 }}>
                <ResponsiveContainer width="100%" height={300}>
                    <AreaChart data={data.error.trend}>
                        <CartesianGrid strokeDasharray="3 3" />
                        <XAxis dataKey="date" />
                        <YAxis />
                        <Tooltip />
                        <Legend />
                        <Area type="monotone" dataKey="errors" stroke="#ff4d4f" fill="#ff4d4f" fillOpacity={0.3} />
                        <Area type="monotone" dataKey="total" stroke="#1677ff" fill="#1677ff" fillOpacity={0.1} />
                    </AreaChart>
                </ResponsiveContainer>
            </Card>
            {data.error.summary.top_codes && data.error.summary.top_codes.length > 0 && (
                <Card title="Top Error Codes">
                    <ResponsiveContainer width="100%" height={240}>
                        <BarChart data={data.error.summary.top_codes}>
                            <CartesianGrid strokeDasharray="3 3" />
                            <XAxis dataKey="code" />
                            <YAxis />
                            <Tooltip />
                            <Bar dataKey="count" fill="#ff4d4f" />
                        </BarChart>
                    </ResponsiveContainer>
                </Card>
            )}
        </div>
    );

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Analytics</Title>
                <Select
                    value={period}
                    onChange={setPeriod}
                    style={{ width: 120 }}
                    options={[
                        { label: "24 Hours", value: "24h" },
                        { label: "7 Days", value: "7d" },
                        { label: "30 Days", value: "30d" },
                        { label: "90 Days", value: "90d" },
                    ]}
                />
            </div>
            <Tabs
                items={[
                    { key: "token", label: "Token", children: tokenTab },
                    { key: "cost", label: "Cost", children: costTab },
                    { key: "model", label: "Model", children: modelTab },
                    { key: "latency", label: "Latency", children: latencyTab },
                    { key: "error", label: "Error", children: errorTab },
                ]}
            />
        </div>
    );
}
