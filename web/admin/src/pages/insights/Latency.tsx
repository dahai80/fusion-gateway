import { useState, useEffect } from "react";
import { Card, Spin, Select, Typography, Input, Row, Col, Alert } from "antd";
import {
    ComposedChart,
    Line,
    Bar,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip,
    Legend,
    ResponsiveContainer,
} from "recharts";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert, PERIOD_OPTIONS, windowFor } from "./shared";

const { Title } = Typography;

interface LatencyBucket {
    bucket: string;
    requests: number;
    ttft_p50_ms: number;
    ttft_p95_ms: number;
    tpot_p50_ms: number;
    tpot_p95_ms: number;
    avg_duration_ms: number;
    error_count: number;
}

interface LatencyResponse {
    from: string;
    to: string;
    granularity: string;
    series: LatencyBucket[];
}

const granularityFor = (hours: number) => (hours <= 24 ? "hour" : "day");

export default function Latency() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [data, setData] = useState<LatencyResponse | null>(null);
    const [period, setPeriod] = useState(24);
    const [backend, setBackend] = useState("");
    const [model, setModel] = useState("");
    const [projectID, setProjectID] = useState("");

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        const params: Record<string, unknown> = { ...windowFor(period), granularity: granularityFor(period) };
        if (backend) params.backend = backend;
        if (model) params.model = model;
        if (projectID) params.project_id = projectID;
        client
            .get("/insights/latency", { params })
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
    }, [period, backend, model, projectID]);

    if (unavailable) return <UnavailableAlert />;

    const series = data?.series || [];

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16, gap: 8 }}>
                <Title level={4} style={{ margin: 0 }}>Latency (TTFT / TPOT)</Title>
                <Row gutter={8}>
                    <Col>
                        <Input
                            placeholder="backend"
                            value={backend}
                            onChange={(e) => setBackend(e.target.value)}
                            style={{ width: 120 }}
                            allowClear
                        />
                    </Col>
                    <Col>
                        <Input
                            placeholder="model"
                            value={model}
                            onChange={(e) => setModel(e.target.value)}
                            style={{ width: 140 }}
                            allowClear
                        />
                    </Col>
                    <Col>
                        <Input
                            placeholder="project"
                            value={projectID}
                            onChange={(e) => setProjectID(e.target.value)}
                            style={{ width: 120 }}
                            allowClear
                        />
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
            ) : series.length === 0 ? (
                <Alert type="info" showIcon message="No generation traffic in this window (only requests with a measured TTFT are aggregated)." />
            ) : (
                <>
                    <Card title="Time to First Token (ms)" style={{ marginBottom: 24 }}>
                        <ResponsiveContainer width="100%" height={320}>
                            <ComposedChart data={series}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="bucket" />
                                <YAxis yAxisId="left" unit="ms" />
                                <YAxis yAxisId="right" orientation="right" />
                                <Tooltip />
                                <Legend />
                                <Bar yAxisId="right" dataKey="requests" name="requests" fill="#1677ff" fillOpacity={0.15} />
                                <Line yAxisId="left" type="monotone" dataKey="ttft_p50_ms" name="TTFT p50" stroke="#1677ff" strokeWidth={2} dot={false} />
                                <Line yAxisId="left" type="monotone" dataKey="ttft_p95_ms" name="TTFT p95" stroke="#ff4d4f" strokeWidth={2} dot={false} />
                            </ComposedChart>
                        </ResponsiveContainer>
                    </Card>
                    <Card title="Time Per Output Token (ms)" style={{ marginBottom: 24 }}>
                        <ResponsiveContainer width="100%" height={320}>
                            <ComposedChart data={series}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="bucket" />
                                <YAxis yAxisId="left" unit="ms" />
                                <YAxis yAxisId="right" orientation="right" />
                                <Tooltip />
                                <Legend />
                                <Bar yAxisId="right" dataKey="error_count" name="errors" fill="#faad14" fillOpacity={0.2} />
                                <Line yAxisId="left" type="monotone" dataKey="tpot_p50_ms" name="TPOT p50" stroke="#52c41a" strokeWidth={2} dot={false} />
                                <Line yAxisId="left" type="monotone" dataKey="tpot_p95_ms" name="TPOT p95" stroke="#faad14" strokeWidth={2} dot={false} />
                            </ComposedChart>
                        </ResponsiveContainer>
                    </Card>
                </>
            )}
        </div>
    );
}
