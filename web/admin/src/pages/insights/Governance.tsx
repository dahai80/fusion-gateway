import { useState, useEffect, useCallback } from "react";
import { Card, Typography, Table, Tag, Alert, Statistic, Col, Row, Spin } from "antd";
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from "recharts";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert } from "./shared";

const { Title } = Typography;

interface BudgetRow {
    sub: string;
    day: string;
    used: number;
    budget: number;
    override?: number;
    soft_alerted?: boolean;
    last_seen?: string;
}

interface PIIDayRow {
    day: string;
    count: number;
}

interface RejectRow {
    key_name: string;
    count: number;
}

interface EventRow {
    id: number;
    ts: string;
    level: string;
    event: string;
    detail?: Record<string, unknown>;
}

interface GovernanceReport {
    generated_at: string;
    budgets: BudgetRow[];
    pii_daily: PIIDayRow[];
    rejects_by_key: RejectRow[];
    events: EventRow[];
}

const levelColor = (level: string) =>
    level === "warn" ? "gold" : level === "error" ? "red" : "blue";

export default function Governance() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [report, setReport] = useState<GovernanceReport | null>(null);

    const fetchReport = useCallback(async () => {
        setLoading(true);
        try {
            const res = await client.get("/reports/governance");
            setReport(res.data);
            setUnavailable(false);
        } catch (err) {
            setUnavailable(isUnavailable(err));
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        fetchReport();
    }, [fetchReport]);

    if (unavailable) return <UnavailableAlert />;

    const softAlerted = report?.budgets.filter((b) => b.soft_alerted).length || 0;
    const piiTotal = report?.pii_daily.reduce((sum, d) => sum + d.count, 0) || 0;
    const rejectTotal = report?.rejects_by_key.reduce((sum, r) => sum + r.count, 0) || 0;

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Governance & Spend</Title>
                {report && (
                    <span style={{ color: "var(--ant-color-text-tertiary, #999)", fontSize: 12 }}>
                        generated {report.generated_at.replace("T", " ").slice(0, 19)}
                    </span>
                )}
            </div>
            <Card style={{ marginBottom: 16 }}>
                <Row gutter={16}>
                    <Col span={6}><Statistic title="Metered users" value={report?.budgets.length || 0} /></Col>
                    <Col span={6}><Statistic title="Soft-limit alerts" value={softAlerted} /></Col>
                    <Col span={6}><Statistic title="PII detections (14d)" value={piiTotal} /></Col>
                    <Col span={6}><Statistic title="Rate-limit rejects (7d)" value={rejectTotal} /></Col>
                </Row>
            </Card>
            <Spin spinning={loading}>
                <Card title="Budgets (try-quota)" style={{ marginBottom: 16 }}>
                    <Table
                        dataSource={report?.budgets || []}
                        rowKey="sub"
                        pagination={{ pageSize: 10, showSizeChanger: false }}
                        columns={[
                            { title: "Subject", dataIndex: "sub" },
                            { title: "Window", dataIndex: "day", width: 120 },
                            { title: "Used", dataIndex: "used", width: 100 },
                            { title: "Budget", dataIndex: "budget", width: 100 },
                            {
                                title: "Usage", width: 180,
                                render: (_v: unknown, r: BudgetRow) =>
                                    r.budget > 0 ? `${Math.min(100, Math.round((r.used / r.budget) * 100))}%` : "—",
                            },
                            {
                                title: "Soft alert", dataIndex: "soft_alerted", width: 110,
                                render: (v: boolean | undefined) =>
                                    v ? <Tag color="gold">fired</Tag> : <Tag>quiet</Tag>,
                            },
                        ]}
                    />
                </Card>
                <Card title="PII detections per day (14 days)" style={{ marginBottom: 16 }}>
                    {(report?.pii_daily.length || 0) === 0 ? (
                        <Alert type="info" showIcon message="No PII detections recorded in the window (or persistence is off)." />
                    ) : (
                        <ResponsiveContainer width="100%" height={220}>
                            <BarChart data={report?.pii_daily || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="rgba(128,128,128,0.2)" />
                                <XAxis dataKey="day" tick={{ fontSize: 11 }} />
                                <YAxis allowDecimals={false} tick={{ fontSize: 11 }} />
                                <Tooltip />
                                <Bar dataKey="count" fill="#6366f1" radius={[4, 4, 0, 0]} />
                            </BarChart>
                        </ResponsiveContainer>
                    )}
                </Card>
                <Card title="Rate-limit rejections by key (7 days)" style={{ marginBottom: 16 }}>
                    <Table
                        dataSource={report?.rejects_by_key || []}
                        rowKey="key_name"
                        pagination={false}
                        columns={[
                            { title: "Key", dataIndex: "key_name", render: (v: string) => v || "(anonymous)" },
                            { title: "429s", dataIndex: "count", width: 100 },
                        ]}
                    />
                </Card>
                <Card title="Recent governance events">
                    <Table
                        dataSource={report?.events || []}
                        rowKey="id"
                        pagination={{ pageSize: 10, showSizeChanger: false }}
                        columns={[
                            { title: "Time", dataIndex: "ts", width: 180, render: (v: string) => v.replace("T", " ").slice(0, 19) },
                            {
                                title: "Level", dataIndex: "level", width: 90,
                                render: (v: string) => <Tag color={levelColor(v)}>{v}</Tag>,
                            },
                            { title: "Event", dataIndex: "event", width: 220 },
                            {
                                title: "Detail",
                                render: (_v: unknown, r: EventRow) =>
                                    r.detail ? JSON.stringify(r.detail) : "—",
                            },
                        ]}
                    />
                </Card>
            </Spin>
        </div>
    );
}
