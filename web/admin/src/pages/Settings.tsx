import { useState, useEffect, useCallback } from "react";
import {
    Card, Form, InputNumber, Switch, Button, Spin, Row, Col,
    Typography, message, Divider, Input, Table, Modal, Tag, Space,
    Select, Popconfirm,
} from "antd";
import { EditOutlined, DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import client from "../api/client";

const { Title, Text } = Typography;

interface RatioTierRule {
    max_ratio: number;
    backend: string;
}

interface TokenTierRule {
    max_tokens: number;
    backend: string;
}

interface RoutingConfig {
    mode: string;
    token_threshold: number;
    output_input_ratio_threshold: number;
    ratio_tiers_enabled: boolean;
    ratio_tiers_rules: RatioTierRule[];
    token_tiers_enabled: boolean;
    token_tiers_metric: string;
    token_tiers_rules: TokenTierRule[];
    local_priority_enabled: boolean;
    max_system_memory_ratio: number;
    max_mlx_memory_ratio: number;
    max_concurrent: number;
    circuit_breaker_enabled: boolean;
    fallback_enabled: boolean;
    fallback_cloud_default: string;
}

interface BackendEntry {
    name: string;
    type: string;
    base_url: string;
    api_key: string;
    timeout: string;
    enabled: boolean;
}

interface BackendFormValues {
    base_url: string;
    api_key: string;
    enabled: boolean;
    timeout: string;
}

interface RatioTierFormValues {
    max_ratio: number;
    backend: string;
}

interface TokenTierFormValues {
    max_tokens: number;
    backend: string;
}

export default function Settings() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const [original, setOriginal] = useState<RoutingConfig | null>(null);

    const [backends, setBackends] = useState<BackendEntry[]>([]);
    const [backendsLoading, setBackendsLoading] = useState(false);
    const [editBackend, setEditBackend] = useState<BackendEntry | null>(null);
    const [backendModalOpen, setBackendModalOpen] = useState(false);
    const [backendForm] = Form.useForm<BackendFormValues>();
    const [backendSaving, setBackendSaving] = useState(false);

    // RatioTiers state
    const [ratioRules, setRatioRules] = useState<RatioTierRule[]>([]);
    const [ratioEnabled, setRatioEnabled] = useState(false);
    const [ratioModalOpen, setRatioModalOpen] = useState(false);
    const [ratioEditIndex, setRatioEditIndex] = useState<number | null>(null);
    const [ratioForm] = Form.useForm<RatioTierFormValues>();

    // TokenTiers state
    const [tokenRules, setTokenRules] = useState<TokenTierRule[]>([]);
    const [tokenEnabled, setTokenEnabled] = useState(false);
    const [tokenMetric, setTokenMetric] = useState<string>("total");
    const [tokenModalOpen, setTokenModalOpen] = useState(false);
    const [tokenEditIndex, setTokenEditIndex] = useState<number | null>(null);
    const [tokenForm] = Form.useForm<TokenTierFormValues>();

    const cloudBackends = backends.filter(b => b.type !== "fusion-mlx");
    const backendOptions = cloudBackends.map(b => ({
        label: b.name,
        value: b.name,
    }));

    const fetchConfig = async () => {
        setLoading(true);
        try {
            const res = await client.get("/config/routing");
            const data: RoutingConfig = res.data?.data || res.data;
            const {
                ratio_tiers_rules, ratio_tiers_enabled,
                token_tiers_rules, token_tiers_enabled, token_tiers_metric,
                ...formFields
            } = data as RoutingConfig & Record<string, unknown>;
            form.setFieldsValue(formFields);
            setRatioRules(ratio_tiers_rules || []);
            setRatioEnabled(ratio_tiers_enabled || false);
            setTokenRules(token_tiers_rules || []);
            setTokenEnabled(token_tiers_enabled || false);
            setTokenMetric(token_tiers_metric || "total");
            setOriginal(data);
        } catch (err) {
            console.error("Failed to fetch routing config:", err);
            message.error("Failed to load routing config");
        } finally {
            setLoading(false);
        }
    };

    const fetchBackends = async () => {
        setBackendsLoading(true);
        try {
            const res = await client.get("/config/backends");
            setBackends(res.data?.data || res.data || []);
        } catch (err) {
            console.error("Failed to fetch backends:", err);
            message.error("Failed to load cloud backends");
        } finally {
            setBackendsLoading(false);
        }
    };

    useEffect(() => {
        fetchConfig();
        fetchBackends();
    }, []);

    const buildPayload = useCallback(() => {
        const values = form.getFieldsValue();
        return {
            ...values,
            ratio_tiers_enabled: ratioEnabled,
            ratio_tiers_rules: ratioRules,
            token_tiers_enabled: tokenEnabled,
            token_tiers_metric: tokenMetric,
            token_tiers_rules: tokenRules,
        };
    }, [form, ratioEnabled, ratioRules, tokenEnabled, tokenMetric, tokenRules]);

    const handleSave = async () => {
        try {
            const values = await form.validateFields();
            setSaving(true);
            const payload = {
                ...values,
                ratio_tiers_enabled: ratioEnabled,
                ratio_tiers_rules: ratioRules,
                token_tiers_enabled: tokenEnabled,
                token_tiers_metric: tokenMetric,
                token_tiers_rules: tokenRules,
            };
            await client.put("/config/routing", payload);
            message.success("Routing config saved, hot reload will apply");
            setOriginal(payload);
        } catch (err) {
            console.error("Failed to save routing config:", err);
            message.error("Failed to save routing config");
        } finally {
            setSaving(false);
        }
    };

    const hasChanges = () => {
        if (!original) return true;
        const current = buildPayload();
        return JSON.stringify(current) !== JSON.stringify(original);
    };

    const handleEditBackend = (record: BackendEntry) => {
        setEditBackend(record);
        backendForm.setFieldsValue({
            base_url: record.base_url,
            api_key: "",
            enabled: record.enabled,
            timeout: record.timeout,
        });
        setBackendModalOpen(true);
    };

    const handleToggleBackend = async (record: BackendEntry) => {
        try {
            await client.put(`/config/backends/${record.name}`, {
                enabled: !record.enabled,
            });
            message.success(`${record.name} ${!record.enabled ? "enabled" : "disabled"}`);
            fetchBackends();
        } catch {
            message.error("Failed to toggle backend");
        }
    };

    const handleBackendSave = async () => {
        if (!editBackend) return;
        try {
            const values = await backendForm.validateFields();
            setBackendSaving(true);
            const payload: Record<string, unknown> = {
                base_url: values.base_url,
                enabled: values.enabled,
                timeout: values.timeout,
            };
            if (values.api_key && values.api_key.trim() !== "") {
                payload.api_key = values.api_key;
            }
            await client.put(`/config/backends/${editBackend.name}`, payload);
            message.success(`Backend ${editBackend.name} updated, hot reload will apply`);
            setBackendModalOpen(false);
            fetchBackends();
        } catch (err) {
            console.error("Failed to save backend:", err);
            message.error("Failed to save backend config");
        } finally {
            setBackendSaving(false);
        }
    };

    // RatioTiers handlers
    const handleAddRatioRule = () => {
        setRatioEditIndex(null);
        ratioForm.resetFields();
        setRatioModalOpen(true);
    };

    const handleEditRatioRule = (index: number) => {
        setRatioEditIndex(index);
        ratioForm.setFieldsValue(ratioRules[index]);
        setRatioModalOpen(true);
    };

    const handleDeleteRatioRule = (index: number) => {
        const next = [...ratioRules];
        next.splice(index, 1);
        setRatioRules(next);
    };

    const handleRatioRuleSave = async () => {
        try {
            const values = await ratioForm.validateFields();
            const rule: RatioTierRule = {
                max_ratio: values.max_ratio,
                backend: values.backend,
            };
            if (ratioEditIndex !== null) {
                const next = [...ratioRules];
                next[ratioEditIndex] = rule;
                setRatioRules(next);
            } else {
                setRatioRules(prev => [...prev, rule]);
            }
            setRatioModalOpen(false);
        } catch {
            // validation failed
        }
    };

    // TokenTiers handlers
    const handleAddTokenRule = () => {
        setTokenEditIndex(null);
        tokenForm.resetFields();
        setTokenModalOpen(true);
    };

    const handleEditTokenRule = (index: number) => {
        setTokenEditIndex(index);
        tokenForm.setFieldsValue(tokenRules[index]);
        setTokenModalOpen(true);
    };

    const handleDeleteTokenRule = (index: number) => {
        const next = [...tokenRules];
        next.splice(index, 1);
        setTokenRules(next);
    };

    const handleTokenRuleSave = async () => {
        try {
            const values = await tokenForm.validateFields();
            const rule: TokenTierRule = {
                max_tokens: values.max_tokens,
                backend: values.backend,
            };
            if (tokenEditIndex !== null) {
                const next = [...tokenRules];
                next[tokenEditIndex] = rule;
                setTokenRules(next);
            } else {
                setTokenRules(prev => [...prev, rule]);
            }
            setTokenModalOpen(false);
        } catch {
            // validation failed
        }
    };

    const ratioColumns = [
        {
            title: "Priority",
            key: "priority",
            width: 70,
            render: (_: unknown, __: unknown, index: number) => (
                <Tag color={index === 0 ? "red" : index === 1 ? "orange" : "blue"}>
                    P{index + 1}
                </Tag>
            ),
        },
        {
            title: "Max Ratio (≤)",
            dataIndex: "max_ratio",
            key: "max_ratio",
            width: 140,
            render: (v: number) => <Text strong>{v}</Text>,
        },
        {
            title: "Backend",
            dataIndex: "backend",
            key: "backend",
            render: (v: string) => <Tag color="geekblue">{v}</Tag>,
        },
        {
            title: "Meaning",
            key: "meaning",
            render: (_: unknown, record: RatioTierRule, index: number) => {
                const prevRatio = index > 0 ? ratioRules[index - 1].max_ratio : 0;
                return (
                    <Text type="secondary">
                        ratio ∈ ({prevRatio}, {record.max_ratio}] → {record.backend}
                    </Text>
                );
            },
        },
        {
            title: "Actions",
            key: "actions",
            width: 100,
            render: (_: unknown, __: unknown, index: number) => (
                <Space>
                    <Button type="link" size="small" icon={<EditOutlined />} onClick={() => handleEditRatioRule(index)} />
                    <Popconfirm title="Delete this rule?" onConfirm={() => handleDeleteRatioRule(index)} okText="Yes" cancelText="No">
                        <Button type="link" size="small" danger icon={<DeleteOutlined />} />
                    </Popconfirm>
                </Space>
            ),
        },
    ];

    const tokenColumns = [
        {
            title: "Priority",
            key: "priority",
            width: 70,
            render: (_: unknown, __: unknown, index: number) => (
                <Tag color={index === 0 ? "red" : index === 1 ? "orange" : "blue"}>
                    P{index + 1}
                </Tag>
            ),
        },
        {
            title: "Max Tokens (≤)",
            dataIndex: "max_tokens",
            key: "max_tokens",
            width: 160,
            render: (v: number) => <Text strong>{v === 0 ? "∞" : v.toLocaleString()}</Text>,
        },
        {
            title: "Backend",
            dataIndex: "backend",
            key: "backend",
            render: (v: string) => <Tag color="geekblue">{v}</Tag>,
        },
        {
            title: "Meaning",
            key: "meaning",
            render: (_: unknown, record: TokenTierRule, index: number) => {
                const prevTokens = index > 0 ? tokenRules[index - 1].max_tokens : 0;
                return (
                    <Text type="secondary">
                        {tokenMetric} ∈ ({prevTokens === 0 ? "0" : prevTokens.toLocaleString()}, {record.max_tokens === 0 ? "∞" : record.max_tokens.toLocaleString()}] → {record.backend}
                    </Text>
                );
            },
        },
        {
            title: "Actions",
            key: "actions",
            width: 100,
            render: (_: unknown, __: unknown, index: number) => (
                <Space>
                    <Button type="link" size="small" icon={<EditOutlined />} onClick={() => handleEditTokenRule(index)} />
                    <Popconfirm title="Delete this rule?" onConfirm={() => handleDeleteTokenRule(index)} okText="Yes" cancelText="No">
                        <Button type="link" size="small" danger icon={<DeleteOutlined />} />
                    </Popconfirm>
                </Space>
            ),
        },
    ];

    const backendColumns = [
        {
            title: "Name",
            dataIndex: "name",
            key: "name",
            render: (name: string, record: BackendEntry) => (
                <Space>
                    <Text strong>{name}</Text>
                    {record.type === "fusion-mlx" && <Tag color="green">Local</Tag>}
                    {record.type !== "fusion-mlx" && <Tag color="blue">Cloud</Tag>}
                </Space>
            ),
        },
        {
            title: "Type",
            dataIndex: "type",
            key: "type",
            render: (t: string) => <Tag>{t}</Tag>,
        },
        {
            title: "Base URL",
            dataIndex: "base_url",
            key: "base_url",
            ellipsis: true,
        },
        {
            title: "API Key",
            dataIndex: "api_key",
            key: "api_key",
            render: (key: string) => key ? <Text code>{key}</Text> : <Text type="secondary">—</Text>,
        },
        {
            title: "Enabled",
            dataIndex: "enabled",
            key: "enabled",
            render: (enabled: boolean, record: BackendEntry) => (
                <Switch
                    checked={enabled}
                    onChange={() => handleToggleBackend(record)}
                    checkedChildren="ON"
                    unCheckedChildren="OFF"
                    size="small"
                />
            ),
        },
        {
            title: "Actions",
            key: "actions",
            width: 80,
            render: (_: unknown, record: BackendEntry) => (
                <Button type="link" size="small" icon={<EditOutlined />} onClick={() => handleEditBackend(record)}>
                    Edit
                </Button>
            ),
        },
    ];

    if (loading) {
        return <div style={{ textAlign: "center", padding: 80 }}><Spin size="large" /></div>;
    }

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Settings</Title>
                <Button type="primary" onClick={handleSave} loading={saving} disabled={!hasChanges()}>
                    Save &amp; Apply
                </Button>
            </div>

            <Card title="Cloud Backends" style={{ marginBottom: 24 }}>
                <Text type="secondary" style={{ display: "block", marginBottom: 16 }}>
                    Configure cloud provider URL and API Key. When routing decides "go cloud", these backends are used.
                    Changes are written to config.yaml and applied via hot reload.
                </Text>
                <Table
                    dataSource={backends}
                    columns={backendColumns}
                    rowKey="name"
                    loading={backendsLoading}
                    pagination={false}
                    size="small"
                />
            </Card>

            <Card title="Routing Rules" style={{ marginBottom: 24 }}>
                <Form form={form} layout="vertical">
                    <Form.Item label="Routing Mode" name="mode"
                        tooltip="Local: all requests go to local backend. Cloud: all requests go to cloud. Hybrid: smart routing by token/ratio/hardware rules.">
                        <Select style={{ width: 200 }}>
                            <Select.Option value="hybrid">Hybrid (Smart)</Select.Option>
                            <Select.Option value="local">Local Only</Select.Option>
                            <Select.Option value="cloud">Cloud Only</Select.Option>
                        </Select>
                    </Form.Item>
                    <Row gutter={24}>
                        <Col span={12}>
                            <Form.Item label="Token Threshold" name="token_threshold"
                                tooltip="Requests with total tokens exceeding this value will be routed to cloud">
                                <InputNumber min={1} max={1000000} style={{ width: "100%" }} addonAfter="tokens" />
                            </Form.Item>
                        </Col>
                        <Col span={12}>
                            <Form.Item label="Output/Input Ratio Threshold (fallback)" name="output_input_ratio_threshold"
                                tooltip="When Ratio Tiers disabled, this single threshold is used. Set 0 to disable.">
                                <InputNumber min={0} max={10} step={0.1} precision={2} style={{ width: "100%" }} addonAfter="ratio" />
                            </Form.Item>
                        </Col>
                    </Row>
                </Form>
            </Card>

            <Card
                title={
                    <Space>
                        <span>Token Tiers</span>
                        <Switch
                            checked={tokenEnabled}
                            onChange={setTokenEnabled}
                            checkedChildren="ON"
                            unCheckedChildren="OFF"
                            size="small"
                        />
                    </Space>
                }
                style={{ marginBottom: 24 }}
            >
                <Text type="secondary" style={{ display: "block", marginBottom: 12 }}>
                    When token count exceeds the Token Threshold, route to different cloud backends based on token volume.
                    Rules are matched top-to-bottom: first rule where {tokenMetric} tokens ≤ max_tokens wins.
                    Set max_tokens = 0 as catch-all (no upper bound).
                </Text>
                {tokenEnabled ? (
                    <>
                        <Form layout="inline" style={{ marginBottom: 16 }}>
                            <Form.Item label="Metric" style={{ marginBottom: 0 }}>
                                <Select
                                    value={tokenMetric}
                                    onChange={setTokenMetric}
                                    style={{ width: 160 }}
                                    options={[
                                        { label: "Input Tokens", value: "input" },
                                        { label: "Output Tokens", value: "output" },
                                        { label: "Total Tokens", value: "total" },
                                    ]}
                                />
                            </Form.Item>
                        </Form>
                        <Table
                            dataSource={tokenRules}
                            columns={tokenColumns}
                            rowKey={(_, index) => String(index)}
                            pagination={false}
                            size="small"
                            locale={{ emptyText: "No tier rules configured" }}
                        />
                        <Divider style={{ margin: "12px 0" }} />
                        <Button type="dashed" icon={<PlusOutlined />} onClick={handleAddTokenRule}>
                            Add Token Tier Rule
                        </Button>
                    </>
                ) : (
                    <Text type="secondary">Enable Token Tiers to configure segmented routing by token count.</Text>
                )}
            </Card>

            <Card
                title={
                    <Space>
                        <span>Ratio Tiers</span>
                        <Switch
                            checked={ratioEnabled}
                            onChange={setRatioEnabled}
                            checkedChildren="ON"
                            unCheckedChildren="OFF"
                            size="small"
                        />
                    </Space>
                }
                style={{ marginBottom: 24 }}
            >
                <Text type="secondary" style={{ display: "block", marginBottom: 12 }}>
                    Route requests to different cloud backends based on predicted output/input token ratio.
                    Rules are matched top-to-bottom: first rule where ratio ≤ max_ratio wins.
                    Example: ratio ≤ 0.6 → volcengine, ≤ 0.8 → dashscope, ≤ 0.9 → qianfan.
                </Text>
                {ratioEnabled ? (
                    <>
                        <Table
                            dataSource={ratioRules}
                            columns={ratioColumns}
                            rowKey={(_, index) => String(index)}
                            pagination={false}
                            size="small"
                            locale={{ emptyText: "No tier rules configured" }}
                        />
                        <Divider style={{ margin: "12px 0" }} />
                        <Button type="dashed" icon={<PlusOutlined />} onClick={handleAddRatioRule}>
                            Add Ratio Tier Rule
                        </Button>
                    </>
                ) : (
                    <Text type="secondary">Enable Ratio Tiers to configure segmented routing by output/input ratio.</Text>
                )}
            </Card>

            <Card title="Local Priority" style={{ marginBottom: 24 }}>
                <Form form={form} layout="vertical">
                    <Form.Item name="local_priority_enabled" valuePropName="checked"
                        tooltip="Enable local-first routing when hardware load is healthy">
                        <Switch checkedChildren="ON" unCheckedChildren="OFF" />
                    </Form.Item>
                    <Row gutter={24}>
                        <Col span={8}>
                            <Form.Item label="Max System Memory Ratio" name="max_system_memory_ratio"
                                tooltip="Force cloud when system memory usage exceeds this ratio">
                                <InputNumber min={0.1} max={0.99} step={0.05} precision={2} style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                        <Col span={8}>
                            <Form.Item label="Max MLX Memory Ratio" name="max_mlx_memory_ratio"
                                tooltip="Force cloud when MLX memory usage exceeds this ratio">
                                <InputNumber min={0.1} max={0.99} step={0.05} precision={2} style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                        <Col span={8}>
                            <Form.Item label="Max Concurrent Requests" name="max_concurrent"
                                tooltip="Force cloud when local concurrent requests exceed this limit">
                                <InputNumber min={1} max={100} style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                    </Row>
                </Form>
            </Card>

            <Card title="Circuit Breaker & Fallback">
                <Form form={form} layout="vertical">
                    <Row gutter={24}>
                        <Col span={8}>
                            <Form.Item name="circuit_breaker_enabled" valuePropName="checked"
                                tooltip="Enable circuit breaker to force cloud when local model fails repeatedly">
                                <Switch checkedChildren="ON" unCheckedChildren="OFF" />
                            </Form.Item>
                        </Col>
                        <Col span={8}>
                            <Form.Item name="fallback_enabled" valuePropName="checked"
                                tooltip="Enable fallback to cloud when local model is unavailable">
                                <Switch checkedChildren="ON" unCheckedChildren="OFF" />
                            </Form.Item>
                        </Col>
                        <Col span={8}>
                            <Form.Item label="Default Cloud Backend" name="fallback_cloud_default"
                                tooltip="Cloud backend name used as fallback target">
                                <Input style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                    </Row>
                    <Divider style={{ margin: "8px 0 16px" }} />
                    <Button type="primary" onClick={handleSave} loading={saving} disabled={!hasChanges()}>
                        Save &amp; Apply
                    </Button>
                    <Text type="secondary" style={{ marginLeft: 12 }}>
                        Changes are written to config.yaml and applied via hot reload
                    </Text>
                </Form>
            </Card>

            <Modal
                title={editBackend ? `Edit Backend: ${editBackend.name}` : "Edit Backend"}
                open={backendModalOpen}
                onOk={handleBackendSave}
                onCancel={() => setBackendModalOpen(false)}
                confirmLoading={backendSaving}
                width={600}
                destroyOnClose
            >
                <Form form={backendForm} layout="vertical">
                    <Form.Item label="Base URL" name="base_url" rules={[{ required: true, message: "Required" }]}>
                        <Input placeholder="https://api.example.com/v1" />
                    </Form.Item>
                    <Form.Item label="API Key" name="api_key"
                        tooltip="Leave empty to keep current key unchanged">
                        <Input.Password placeholder="Enter new key or leave empty to keep current" />
                    </Form.Item>
                    <Form.Item label="Timeout" name="timeout">
                        <Input placeholder="60s" />
                    </Form.Item>
                    <Form.Item name="enabled" valuePropName="checked" label="Enabled">
                        <Switch checkedChildren="ON" unCheckedChildren="OFF" />
                    </Form.Item>
                </Form>
            </Modal>

            <Modal
                title={ratioEditIndex !== null ? "Edit Ratio Tier Rule" : "Add Ratio Tier Rule"}
                open={ratioModalOpen}
                onOk={handleRatioRuleSave}
                onCancel={() => setRatioModalOpen(false)}
                width={480}
                destroyOnClose
            >
                <Form form={ratioForm} layout="vertical">
                    <Form.Item label="Max Ratio" name="max_ratio"
                        rules={[{ required: true, message: "Required" }]}
                        tooltip="When predicted output/input ratio ≤ this value, route to the selected backend">
                        <InputNumber min={0.01} max={10} step={0.1} precision={2} style={{ width: "100%" }} />
                    </Form.Item>
                    <Form.Item label="Backend" name="backend"
                        rules={[{ required: true, message: "Required" }]}
                        tooltip="Cloud backend to route to when this tier matches">
                        <Select
                            placeholder="Select cloud backend"
                            options={backendOptions}
                            showSearch
                        />
                    </Form.Item>
                    <Text type="secondary">
                        Rules are matched top-to-bottom. Sort by max_ratio ascending so lower ratios get higher priority.
                    </Text>
                </Form>
            </Modal>

            <Modal
                title={tokenEditIndex !== null ? "Edit Token Tier Rule" : "Add Token Tier Rule"}
                open={tokenModalOpen}
                onOk={handleTokenRuleSave}
                onCancel={() => setTokenModalOpen(false)}
                width={480}
                destroyOnClose
            >
                <Form form={tokenForm} layout="vertical">
                    <Form.Item label="Max Tokens" name="max_tokens"
                        rules={[{ required: true, message: "Required" }]}
                        tooltip="When token count (by selected metric) ≤ this value, route to the selected backend. Set 0 for no upper bound (catch-all).">
                        <InputNumber min={0} max={1000000} step={1000} style={{ width: "100%" }} addonAfter="tokens" />
                    </Form.Item>
                    <Form.Item label="Backend" name="backend"
                        rules={[{ required: true, message: "Required" }]}
                        tooltip="Cloud backend to route to when this tier matches">
                        <Select
                            placeholder="Select cloud backend"
                            options={backendOptions}
                            showSearch
                        />
                    </Form.Item>
                    <Text type="secondary">
                        Rules are matched top-to-bottom. Sort by max_tokens ascending so lower token counts get higher priority.
                        Set max_tokens = 0 as the last rule to catch all remaining requests.
                    </Text>
                </Form>
            </Modal>
        </div>
    );
}
