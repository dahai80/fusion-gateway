import { useState, useEffect, useCallback } from "react";
import { Card, Typography, Select, Input, Table, Tag, Button, Popconfirm, message } from "antd";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert } from "./shared";

const { Title } = Typography;

interface Finding {
    id: number;
    generated_at: string;
    rule: string;
    scope: Record<string, unknown>;
    evidence: Record<string, unknown>;
    action: Record<string, unknown>;
    status: string;
    applied_at?: string;
    applied_config?: string;
}

export default function Optimize() {
    const [messageApi, messageCtx] = message.useMessage();
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [rows, setRows] = useState<Finding[]>([]);
    const [status, setStatus] = useState("new");
    const [rule, setRule] = useState("");

    const fetchFindings = useCallback(async () => {
        setLoading(true);
        const params: Record<string, unknown> = {};
        if (status) params.status = status;
        if (rule) params.rule = rule;
        try {
            const res = await client.get("/optimization/findings", { params });
            setRows(res.data.findings || []);
            setUnavailable(false);
        } catch (err) {
            setUnavailable(isUnavailable(err));
        } finally {
            setLoading(false);
        }
    }, [status, rule]);

    useEffect(() => {
        fetchFindings();
    }, [fetchFindings]);

    const applyFinding = async (f: Finding) => {
        try {
            await client.post(`/optimization/findings/${f.id}/apply`);
            messageApi.success(`Finding #${f.id} applied — models.supply updated`);
            fetchFindings();
        } catch (err) {
            const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error || "apply failed";
            messageApi.error(detail);
        }
    };

    const dismissFinding = async (f: Finding) => {
        try {
            await client.post(`/optimization/findings/${f.id}/dismiss`);
            messageApi.success(`Finding #${f.id} dismissed`);
            fetchFindings();
        } catch {
            messageApi.error("dismiss failed");
        }
    };

    if (unavailable) return <UnavailableAlert />;

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Optimization Findings</Title>
                <div style={{ display: "flex", gap: 8 }}>
                    <Input
                        placeholder="rule"
                        value={rule}
                        onChange={(e) => setRule(e.target.value)}
                        style={{ width: 180 }}
                        allowClear
                    />
                    <Select
                        value={status}
                        onChange={setStatus}
                        style={{ width: 140 }}
                        options={[
                            { label: "New", value: "new" },
                            { label: "Applied", value: "applied" },
                            { label: "Dismissed", value: "dismissed" },
                            { label: "All", value: "" },
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
                        { title: "Generated", dataIndex: "generated_at", width: 180, render: (v: string) => v.replace("T", " ").slice(0, 19) },
                        {
                            title: "Rule",
                            dataIndex: "rule",
                            width: 220,
                            render: (v: string) => (
                                <Tag color={v === "MIGRATE_TO_LOCAL" ? "green" : "blue"}>{v}</Tag>
                            ),
                        },
                        {
                            title: "Scope",
                            dataIndex: "scope",
                            render: (v: Finding["scope"]) => <code style={{ fontSize: 12 }}>{JSON.stringify(v)}</code>,
                        },
                        {
                            title: "Evidence",
                            dataIndex: "evidence",
                            render: (v: Finding["evidence"]) => <code style={{ fontSize: 12 }}>{JSON.stringify(v)}</code>,
                        },
                        {
                            title: "Status",
                            dataIndex: "status",
                            width: 100,
                            render: (v: string) => (
                                <Tag color={v === "applied" ? "green" : v === "dismissed" ? "default" : "orange"}>{v}</Tag>
                            ),
                        },
                        {
                            title: "Actions",
                            width: 200,
                            render: (_v: unknown, f: Finding) =>
                                f.status !== "new" ? (
                                    f.applied_at ? <span style={{ fontSize: 12 }}>applied {f.applied_at.slice(0, 10)}</span> : "-"
                                ) : (
                                    <>
                                        {f.rule === "MIGRATE_TO_LOCAL" && (
                                            <Popconfirm
                                                title="Prepend fusion-mlx to this model's models.supply?"
                                                description="Existing entries stay as fallback. Takes effect immediately."
                                                onConfirm={() => applyFinding(f)}
                                            >
                                                <Button type="link" size="small">Apply</Button>
                                            </Popconfirm>
                                        )}
                                        <Popconfirm title="Dismiss this finding?" onConfirm={() => dismissFinding(f)}>
                                            <Button type="link" size="small" danger>Dismiss</Button>
                                        </Popconfirm>
                                    </>
                                ),
                        },
                    ]}
                />
            </Card>
            {messageCtx}
        </div>
    );
}
