import { useState, useCallback } from "react";
import { Card, Typography, Input, Row, Col, Table, Tag, Button, Select, Form } from "antd";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert, windowFor } from "./shared";

const { Title } = Typography;

interface SearchRow {
    id: string;
    ts: string;
    request_id: string;
    api_key_name?: string;
    project_id?: string;
    endpoint: string;
    is_stream: boolean;
    model: string;
    model_mapped?: string;
    backend: string;
    route_reason?: string;
    status_code: number;
    is_success: boolean;
    error_message?: string;
    input_tokens: number;
    output_tokens: number;
    total_tokens: number;
    cost: number;
    duration_ms: number;
    ttft_ms: number;
    tpot_ms: number;
    chunk_count: number;
    tokens_per_sec: number;
    client_ip?: string;
    user_agent?: string;
}

interface PayloadResponse {
    id: string;
    ts: string;
    req_body?: unknown;
    resp_body?: unknown;
    req_raw?: string;
    resp_raw?: string;
    req_truncated: boolean;
    resp_truncated: boolean;
}

const PAGE_SIZE = 50;

const bodyToText = (p: PayloadResponse) => {
    const parts: string[] = [];
    const reqText = p.req_raw && p.req_raw.length > 0
        ? p.req_raw
        : p.req_body ? JSON.stringify(p.req_body, null, 2) : "";
    const respText = p.resp_raw && p.resp_raw.length > 0
        ? p.resp_raw
        : p.resp_body ? JSON.stringify(p.resp_body, null, 2) : "";
    if (reqText) parts.push("── request ──\n" + reqText + (p.req_truncated ? "\n(truncated)" : ""));
    if (respText) parts.push("── response ──\n" + respText + (p.resp_truncated ? "\n(truncated)" : ""));
    return parts.join("\n\n") || "no captured bodies for this row";
};

export default function Explorer() {
    const [loading, setLoading] = useState(false);
    const [unavailable, setUnavailable] = useState(false);
    const [rows, setRows] = useState<SearchRow[]>([]);
    const [total, setTotal] = useState(0);
    const [page, setPage] = useState(1);
    const [expanded, setExpanded] = useState<Record<string, string>>({});
    const [form] = Form.useForm();

    const runSearch = useCallback(async (pageNum = 1) => {
        setLoading(true);
        const v = form.getFieldsValue();
        const body: Record<string, unknown> = {
            ...windowFor(v.period || 24),
            q: v.q || "",
            endpoint: v.endpoint || "",
            backend: v.backend || "",
            model: v.model || "",
            project_id: v.project_id || "",
            api_key_name: v.api_key_name || "",
            status_code: v.status_code || 0,
            min_duration_ms: v.min_duration_ms || 0,
            limit: PAGE_SIZE,
            offset: (pageNum - 1) * PAGE_SIZE,
        };
        if (v.is_stream !== undefined && v.is_stream !== null) body.is_stream = v.is_stream;
        if (v.is_success !== undefined && v.is_success !== null) body.is_success = v.is_success;
        try {
            const res = await client.post("/insights/logs/search", body);
            setRows(res.data.rows || []);
            setTotal(res.data.total || 0);
            setPage(pageNum);
            setUnavailable(false);
            setExpanded({});
        } catch (err) {
            setUnavailable(isUnavailable(err));
        } finally {
            setLoading(false);
        }
    }, [form]);

    const expandRow = async (record: SearchRow) => {
        if (expanded[record.id] !== undefined) return;
        try {
            const res = await client.get(`/insights/logs/${record.id}/payload`);
            setExpanded((prev) => ({ ...prev, [record.id]: bodyToText(res.data) }));
        } catch {
            setExpanded((prev) => ({ ...prev, [record.id]: "payload fetch failed" }));
        }
    };

    if (unavailable) return <UnavailableAlert />;

    return (
        <div>
            <Title level={4} style={{ marginBottom: 16 }}>Log Explorer</Title>
            <Card style={{ marginBottom: 16 }}>
                <Form
                    form={form}
                    layout="vertical"
                    onFinish={() => runSearch(1)}
                    initialValues={{ period: 24 }}
                >
                    <Row gutter={12}>
                        <Col span={4}>
                            <Form.Item name="q" label="Search">
                                <Input placeholder="model / error / request id" allowClear />
                            </Form.Item>
                        </Col>
                        <Col span={3}>
                            <Form.Item name="endpoint" label="Endpoint">
                                <Input placeholder="/v1/chat/..." allowClear />
                            </Form.Item>
                        </Col>
                        <Col span={3}>
                            <Form.Item name="backend" label="Backend">
                                <Input allowClear />
                            </Form.Item>
                        </Col>
                        <Col span={3}>
                            <Form.Item name="model" label="Model">
                                <Input allowClear />
                            </Form.Item>
                        </Col>
                        <Col span={3}>
                            <Form.Item name="project_id" label="Project">
                                <Input allowClear />
                            </Form.Item>
                        </Col>
                        <Col span={3}>
                            <Form.Item name="api_key_name" label="API Key">
                                <Input allowClear />
                            </Form.Item>
                        </Col>
                        <Col span={2}>
                            <Form.Item name="status_code" label="Status">
                                <Input type="number" placeholder="e.g. 500" />
                            </Form.Item>
                        </Col>
                        <Col span={2}>
                            <Form.Item name="min_duration_ms" label="Min ms">
                                <Input type="number" />
                            </Form.Item>
                        </Col>
                    </Row>
                    <Row gutter={12}>
                        <Col span={4}>
                            <Form.Item name="is_stream" label="Stream">
                                <Select
                                    allowClear
                                    options={[
                                        { label: "stream", value: true },
                                        { label: "non-stream", value: false },
                                    ]}
                                />
                            </Form.Item>
                        </Col>
                        <Col span={4}>
                            <Form.Item name="is_success" label="Success">
                                <Select
                                    allowClear
                                    options={[
                                        { label: "success", value: true },
                                        { label: "failed", value: false },
                                    ]}
                                />
                            </Form.Item>
                        </Col>
                        <Col span={4}>
                            <Form.Item name="period" label="Window">
                                <Select
                                    options={[
                                        { label: "24 Hours", value: 24 },
                                        { label: "3 Days", value: 72 },
                                        { label: "7 Days", value: 168 },
                                        { label: "30 Days", value: 720 },
                                    ]}
                                />
                            </Form.Item>
                        </Col>
                        <Col span={4} style={{ display: "flex", alignItems: "flex-end", paddingBottom: 24 }}>
                            <Button type="primary" htmlType="submit" loading={loading}>
                                Search
                            </Button>
                        </Col>
                    </Row>
                </Form>
            </Card>
            <Card>
                <Table
                    dataSource={rows}
                    rowKey="id"
                    loading={loading}
                    size="small"
                    pagination={{
                        current: page,
                        pageSize: PAGE_SIZE,
                        total,
                        showSizeChanger: false,
                        onChange: (p) => runSearch(p),
                    }}
                    expandable={{
                        expandedRowRender: (record) => (
                            <pre style={{ maxHeight: 400, overflow: "auto", fontSize: 12 }}>
                                {expanded[record.id] ?? "loading…"}
                            </pre>
                        ),
                        onExpand: (_expanded, record) => expandRow(record),
                    }}
                    columns={[
                        { title: "Time", dataIndex: "ts", width: 200, render: (v: string) => v.replace("T", " ").replace(/Z$/, "") },
                        { title: "Endpoint", dataIndex: "endpoint", width: 160, ellipsis: true },
                        { title: "Model", dataIndex: "model", ellipsis: true, render: (v: string, r: SearchRow) => r.model_mapped ? <span title={`mapped: ${r.model_mapped}`}>{v}</span> : v },
                        { title: "Backend", dataIndex: "backend", width: 110 },
                        {
                            title: "Status",
                            dataIndex: "status_code",
                            width: 80,
                            render: (v: number, r: SearchRow) => (
                                <Tag color={r.is_success ? "green" : "red"}>{v}</Tag>
                            ),
                        },
                        { title: "Stream", dataIndex: "is_stream", width: 70, render: (v: boolean) => (v ? "sse" : "json") },
                        { title: "In", dataIndex: "input_tokens", width: 80, render: (v: number) => v.toLocaleString() },
                        { title: "Out", dataIndex: "output_tokens", width: 80, render: (v: number) => v.toLocaleString() },
                        { title: "TTFT", dataIndex: "ttft_ms", width: 80, render: (v: number) => (v > 0 ? `${Math.round(v)}ms` : "-") },
                        { title: "TPOT", dataIndex: "tpot_ms", width: 80, render: (v: number) => (v > 0 ? `${v.toFixed(1)}ms` : "-") },
                        { title: "Dur", dataIndex: "duration_ms", width: 80, render: (v: number) => (v > 0 ? `${Math.round(v)}ms` : "-") },
                        { title: "Cost", dataIndex: "cost", width: 80, render: (v: number) => v.toFixed(4) },
                        { title: "Key", dataIndex: "api_key_name", ellipsis: true },
                    ]}
                />
            </Card>
        </div>
    );
}
