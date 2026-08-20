import { useState, useEffect, useCallback } from "react";
import { Table, Button, Space, Select, Input, DatePicker, Tag, message, Typography } from "antd";
import { DownloadOutlined, ReloadOutlined, SearchOutlined } from "@ant-design/icons";
import type { ColumnsType } from "antd/es/table";
import client from "../api/client";

const { Title } = Typography;
const { RangePicker } = DatePicker;

interface LogEntry {
    id: string;
    request_id: string;
    timestamp: string;
    api_key_name: string;
    model: string;
    channel_name: string;
    input_tokens: number;
    output_tokens: number;
    total_tokens: number;
    cost: number;
    latency_ms: number;
    status_code: number;
    is_success: boolean;
    route_reason: string;
}

interface LogFilters {
    key: string;
    model: string;
    channel: string;
    status: string | undefined;
    time_start: string | undefined;
    time_end: string | undefined;
}

export default function Logs() {
    const [logs, setLogs] = useState<LogEntry[]>([]);
    const [loading, setLoading] = useState(false);
    const [total, setTotal] = useState(0);
    const [page, setPage] = useState(1);
    const [pageSize, setPageSize] = useState(20);
    const [models, setModels] = useState<string[]>([]);
    const [channels, setChannels] = useState<string[]>([]);
    const [filters, setFilters] = useState<LogFilters>({
        key: "",
        model: "",
        channel: "",
        status: undefined,
        time_start: undefined,
        time_end: undefined,
    });

    const fetchLogs = useCallback(async () => {
        setLoading(true);
        try {
            const params: Record<string, unknown> = {
                page,
                limit: pageSize,
            };
            if (filters.key) params.key_name = filters.key;
            if (filters.model) params.model = filters.model;
            if (filters.channel) params.channel = filters.channel;
            if (filters.status) params.status = filters.status;
            if (filters.time_start) params.from = filters.time_start;
            if (filters.time_end) params.to = filters.time_end;
            const res = await client.get("/logs", { params });
            const data = res.data?.data || res.data || {};
            setLogs(data.logs || []);
            setTotal(data.total || 0);
        } catch {
            message.error("Failed to fetch logs");
        } finally {
            setLoading(false);
        }
    }, [page, pageSize, filters]);

    const fetchFilters = async () => {
        try {
            const res = await client.get("/logs/filters");
            const data = res.data?.data || res.data || {};
            setModels(data.models || []);
            setChannels(data.channels || []);
        } catch {
            // silent — filters endpoint optional, dropdowns stay empty
        }
    };

    useEffect(() => {
        fetchLogs();
    }, [fetchLogs]);

    useEffect(() => {
        fetchFilters();
    }, []);

    const handleExport = async (format: string) => {
        try {
            const params: Record<string, unknown> = { format, ...filters };
            const res = await client.get("/logs/export", { params, responseType: "blob" });
            const url = window.URL.createObjectURL(new Blob([res.data]));
            const link = document.createElement("a");
            link.href = url;
            link.download = `logs.${format}`;
            link.click();
            window.URL.revokeObjectURL(url);
            message.success("Export started");
        } catch {
            message.error("Export failed");
        }
    };

    const columns: ColumnsType<LogEntry> = [
        { title: "ID", dataIndex: "id", key: "id", width: 80, ellipsis: true },
        { title: "Request ID", dataIndex: "request_id", key: "request_id", width: 140, ellipsis: true },
        { title: "Key", dataIndex: "api_key_name", key: "api_key_name", width: 120 },
        {
            title: "Model",
            dataIndex: "model",
            key: "model",
            width: 140,
            render: (m: string) => <Tag>{m}</Tag>,
        },
        {
            title: "Channel",
            dataIndex: "channel_name",
            key: "channel_name",
            width: 120,
            render: (c: string) => <Tag color="blue">{c}</Tag>,
        },
        { title: "Prompt", dataIndex: "input_tokens", key: "input_tokens", width: 80, sorter: true },
        { title: "Completion", dataIndex: "output_tokens", key: "output_tokens", width: 90, sorter: true },
        { title: "Total", dataIndex: "total_tokens", key: "total_tokens", width: 80, sorter: true },
        {
            title: "Cost",
            dataIndex: "cost",
            key: "cost",
            width: 80,
            render: (c: number) => `$${c?.toFixed(4) || "0"}`,
        },
        {
            title: "Latency",
            dataIndex: "latency_ms",
            key: "latency_ms",
            width: 80,
            render: (l: number) => `${l}ms`,
            sorter: true,
        },
        {
            title: "Route",
            dataIndex: "route_reason",
            key: "route_reason",
            width: 90,
            ellipsis: true,
            render: (r: string) => (
                <Tag color={r === "local" || r?.includes("local") ? "green" : "orange"}>{r || "-"}</Tag>
            ),
        },
        {
            title: "Status",
            dataIndex: "status_code",
            key: "status_code",
            width: 70,
            render: (s: number, row: LogEntry) => (
                <Tag color={row.is_success ? "green" : "red"}>{s}</Tag>
            ),
        },
        { title: "Time", dataIndex: "timestamp", key: "timestamp", width: 160 },
    ];

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Request Logs</Title>
                <Space>
                    <Button icon={<ReloadOutlined />} onClick={fetchLogs}>Refresh</Button>
                    <Button icon={<DownloadOutlined />} onClick={() => handleExport("csv")}>CSV</Button>
                    <Button icon={<DownloadOutlined />} onClick={() => handleExport("json")}>JSON</Button>
                </Space>
            </div>
            <div style={{ marginBottom: 16, display: "flex", gap: 8, flexWrap: "wrap" }}>
                <Input
                    placeholder="API Key"
                    value={filters.key}
                    onChange={(e) => setFilters({ ...filters, key: e.target.value })}
                    style={{ width: 160 }}
                    allowClear
                />
                <Select
                    placeholder="Model"
                    value={filters.model || undefined}
                    onChange={(v) => setFilters({ ...filters, model: v || "" })}
                    style={{ width: 180 }}
                    allowClear
                    options={models.map((m) => ({ label: m, value: m }))}
                />
                <Select
                    placeholder="Channel"
                    value={filters.channel || undefined}
                    onChange={(v) => setFilters({ ...filters, channel: v || "" })}
                    style={{ width: 150 }}
                    allowClear
                    options={channels.map((c) => ({ label: c, value: c }))}
                />
                <Select
                    placeholder="Status"
                    value={filters.status}
                    onChange={(v) => setFilters({ ...filters, status: v })}
                    style={{ width: 120 }}
                    allowClear
                    options={[
                        { label: "Success", value: "success" },
                        { label: "Error", value: "error" },
                    ]}
                />
                <RangePicker
                    showTime
                    onChange={(dates) => {
                        setFilters({
                            ...filters,
                            time_start: dates?.[0]?.toISOString() || undefined,
                            time_end: dates?.[1]?.toISOString() || undefined,
                        });
                    }}
                />
                <Button type="primary" icon={<SearchOutlined />} onClick={() => { setPage(1); fetchLogs(); }}>
                    Search
                </Button>
            </div>
            <Table
                rowKey="id"
                columns={columns}
                dataSource={logs}
                loading={loading}
                scroll={{ x: 1500 }}
                pagination={{
                    current: page,
                    pageSize,
                    total,
                    showSizeChanger: true,
                    showTotal: (t) => `Total ${t}`,
                    onChange: (p, ps) => {
                        setPage(p);
                        setPageSize(ps);
                    },
                }}
            />
        </div>
    );
}
