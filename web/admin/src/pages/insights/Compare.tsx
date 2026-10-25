import { useState, useEffect } from "react";
import { Card, Spin, Select, Typography, Input, Row, Col, Alert, Table, Tag } from "antd";
import {
    BarChart,
    Bar,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip,
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
}

interface CompareResponse {
    from: string;
    to: string;
    rows: CompareRow[];
}

export default function Compare() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [data, setData] = useState<CompareResponse | null>(null);
    const [period, setPeriod] = useState(168);
    const [projectID, setProjectID] = useState("");

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        const params: Record<string, unknown> = windowFor(period);
        if (projectID) params.project_id = projectID;
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
    }, [period, projectID]);

    if (unavailable) return <UnavailableAlert />;

    const rows = (data?.rows || []).map((r) => ({
        ...r,
        label: `${r.backend}/${r.model}`,
    }));

    const numSorter = (key: keyof CompareRow) =>
        (a: CompareRow, b: CompareRow) => (a[key] as number) - (b[key] as number);

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
                                <Tooltip />
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
                                    render: (v: string, r: CompareRow) => (
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
                                { title: "TPOT p50", dataIndex: "tpot_p50_ms", sorter: numSorter("tpot_p50_ms"), render: (v: number) => `${v.toFixed(1)} ms` },
                                { title: "TPOT p95", dataIndex: "tpot_p95_ms", sorter: numSorter("tpot_p95_ms"), render: (v: number) => `${v.toFixed(1)} ms` },
                                { title: "Tok/s", dataIndex: "avg_tokens_per_sec", sorter: numSorter("avg_tokens_per_sec"), render: (v: number) => v.toFixed(1) },
                                { title: "Avg In", dataIndex: "avg_input_tokens", render: (v: number) => Math.round(v).toLocaleString() },
                                { title: "Avg Out", dataIndex: "avg_output_tokens", render: (v: number) => Math.round(v).toLocaleString() },
                                { title: "Cost", dataIndex: "cost_total", sorter: numSorter("cost_total"), render: (v: number) => v.toFixed(4) },
                            ]}
                        />
                    </Card>
                </>
            )}
        </div>
    );
}
