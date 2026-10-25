import { useState, useEffect, useCallback } from "react";
import { Card, Typography, Select, Table, Tag, Button, Popconfirm, Modal, message } from "antd";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert } from "./shared";

const { Title } = Typography;

interface ReportRow {
    id: number;
    period: string;
    range_start: string;
    range_end: string;
    created_at: string;
    content_bytes: number;
    content_md?: string;
}

export default function Reports() {
    const [messageApi, messageCtx] = message.useMessage();
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [rows, setRows] = useState<ReportRow[]>([]);
    const [period, setPeriod] = useState("");
    const [generating, setGenerating] = useState("");
    const [detail, setDetail] = useState<ReportRow | null>(null);

    const fetchReports = useCallback(async () => {
        setLoading(true);
        const params: Record<string, unknown> = { limit: 100 };
        if (period) params.period = period;
        try {
            const res = await client.get("/reports", { params });
            setRows(res.data.reports || []);
            setUnavailable(false);
        } catch (err) {
            setUnavailable(isUnavailable(err));
        } finally {
            setLoading(false);
        }
    }, [period]);

    useEffect(() => {
        fetchReports();
    }, [fetchReports]);

    const generate = async (p: string) => {
        setGenerating(p);
        try {
            await client.post("/reports/generate", { period: p });
            messageApi.success(`${p} report generated`);
            fetchReports();
        } catch (err) {
            const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error || "generate failed";
            messageApi.error(detail);
        } finally {
            setGenerating("");
        }
    };

    const viewReport = async (r: ReportRow) => {
        try {
            const res = await client.get(`/reports/${r.id}`);
            setDetail(res.data);
        } catch {
            messageApi.error("failed to load report");
        }
    };

    if (unavailable) return <UnavailableAlert />;

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Periodic Reports</Title>
                <div style={{ display: "flex", gap: 8 }}>
                    <Popconfirm
                        title="Generate the daily report now?"
                        description="Idempotent — an existing report for the window is returned as-is."
                        onConfirm={() => generate("daily")}
                    >
                        <Button loading={generating === "daily"} disabled={!!generating}>
                            Generate Daily
                        </Button>
                    </Popconfirm>
                    <Popconfirm
                        title="Generate the weekly report now?"
                        description="Idempotent — an existing report for the window is returned as-is."
                        onConfirm={() => generate("weekly")}
                    >
                        <Button loading={generating === "weekly"} disabled={!!generating}>
                            Generate Weekly
                        </Button>
                    </Popconfirm>
                    <Select
                        value={period}
                        onChange={setPeriod}
                        style={{ width: 140 }}
                        options={[
                            { label: "All", value: "" },
                            { label: "Daily", value: "daily" },
                            { label: "Weekly", value: "weekly" },
                        ]}
                    />
                </div>
            </div>
            <Card>
                <Table
                    dataSource={rows}
                    rowKey="id"
                    loading={loading}
                    pagination={{ pageSize: 20, showSizeChanger: false }}
                    columns={[
                        { title: "ID", dataIndex: "id", width: 60 },
                        {
                            title: "Period",
                            dataIndex: "period",
                            width: 100,
                            render: (v: string) => <Tag color={v === "daily" ? "blue" : "purple"}>{v}</Tag>,
                        },
                        { title: "Window", width: 240, render: (_v: unknown, r: ReportRow) => `${r.range_start.slice(0, 10)} → ${r.range_end.slice(0, 10)}` },
                        { title: "Created", dataIndex: "created_at", width: 180, render: (v: string) => v.replace("T", " ").slice(0, 19) },
                        { title: "Size", dataIndex: "content_bytes", width: 100, render: (v: number) => `${v} B` },
                        {
                            title: "Actions",
                            render: (_v: unknown, r: ReportRow) => (
                                <Button type="link" size="small" onClick={() => viewReport(r)}>
                                    View
                                </Button>
                            ),
                        },
                    ]}
                />
            </Card>
            <Modal
                open={!!detail}
                title={detail ? `${detail.period} report — ${detail.range_start.slice(0, 10)}` : ""}
                onCancel={() => setDetail(null)}
                footer={null}
                width={800}
            >
                <pre style={{ maxHeight: 600, overflow: "auto", fontSize: 12, whiteSpace: "pre-wrap" }}>
                    {detail?.content_md || ""}
                </pre>
            </Modal>
            {messageCtx}
        </div>
    );
}
