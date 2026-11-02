import { useCallback, useEffect, useState } from "react";
import {
    Button,
    Form,
    Input,
    Modal,
    Popconfirm,
    Select,
    Space,
    Switch,
    Table,
    Tag,
    Typography,
    message,
} from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import client from "../../api/client";

const { Title, Paragraph } = Typography;

interface Backend {
    name: string;
    type: string;
    base_url: string;
    api_key: string;
    timeout: string;
    enabled: boolean;
    models: string[] | null;
}

interface BackendForm {
    name: string;
    type: string;
    base_url: string;
    api_key: string;
    timeout: string;
    models: string[];
    enabled: boolean;
}

const PROVIDER_TYPES = [
    { label: "Anthropic (Claude)", value: "anthropic", baseURL: "https://api.anthropic.com" },
    { label: "OpenAI Compatible", value: "openai-compatible", baseURL: "https://api.openai.com/v1" },
    { label: "Azure OpenAI", value: "azure-openai", baseURL: "" },
    { label: "DeepSeek", value: "deepseek", baseURL: "https://api.deepseek.com/v1" },
    { label: "OpenRouter", value: "openrouter", baseURL: "https://openrouter.ai/api/v1" },
    { label: "DashScope", value: "dashscope", baseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1" },
    { label: "Moonshot", value: "moonshot", baseURL: "https://api.moonshot.cn/v1" },
    { label: "Zhipu", value: "zhipu", baseURL: "https://open.bigmodel.cn/api/paas/v4" },
    { label: "MiniMax", value: "minimax", baseURL: "https://api.minimax.chat/v1" },
    { label: "Baichuan", value: "baichuan", baseURL: "https://api.baichuan-ai.com/v1" },
    { label: "Hunyuan", value: "hunyuan", baseURL: "https://api.hunyuan.cloud.tencent.com/v1" },
    { label: "StepFun", value: "stepfun", baseURL: "https://api.stepfun.com/v1" },
    { label: "Yi", value: "yi", baseURL: "https://api.lingyiwanwu.com/v1" },
    { label: "Volcengine", value: "volcengine", baseURL: "https://ark.cn-beijing.volces.com/api/v3" },
    { label: "Qianfan", value: "qianfan", baseURL: "" },
    { label: "Bedrock", value: "bedrock", baseURL: "" },
    { label: "Vertex", value: "vertex", baseURL: "" },
    { label: "Foundry", value: "foundry", baseURL: "" },
    { label: "Fusion MLX (Local)", value: "fusion-mlx", baseURL: "http://127.0.0.1:11434" },
    { label: "Fusion KB", value: "fusion-kb", baseURL: "" },
    { label: "Fusion Model Hub", value: "fusion-model-hub", baseURL: "" },
];

const typeLabel = (type: string) =>
    PROVIDER_TYPES.find((item) => item.value === type)?.label ?? type;

const errorMessage = (error: unknown, fallback: string) => {
    const response = (error as { response?: { data?: { error?: { message?: string } } } }).response;
    return response?.data?.error?.message ?? fallback;
};

export default function Providers() {
    const [backends, setBackends] = useState<Backend[]>([]);
    const [loading, setLoading] = useState(false);
    const [saving, setSaving] = useState(false);
    const [modalOpen, setModalOpen] = useState(false);
    const [editing, setEditing] = useState<Backend | null>(null);
    const [form] = Form.useForm<BackendForm>();

    const fetchBackends = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<Backend[]>("/config/backends");
            setBackends(Array.isArray(data) ? data : []);
        } catch (error) {
            message.error(errorMessage(error, "Failed to load providers"));
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        fetchBackends();
    }, [fetchBackends]);

    const openCreate = () => {
        setEditing(null);
        form.resetFields();
        form.setFieldsValue({
            type: "anthropic",
            base_url: PROVIDER_TYPES[0].baseURL,
            timeout: "60s",
            models: [],
            enabled: true,
        });
        setModalOpen(true);
    };

    const openEdit = (record: Backend) => {
        setEditing(record);
        form.resetFields();
        form.setFieldsValue({
            name: record.name,
            type: record.type,
            base_url: record.base_url,
            api_key: "",
            timeout: record.timeout,
            models: record.models ?? [],
            enabled: record.enabled,
        });
        setModalOpen(true);
    };

    const submit = async () => {
        let values: BackendForm;
        try {
            values = await form.validateFields();
        } catch {
            return;
        }
        setSaving(true);
        try {
            if (editing) {
                const payload: Record<string, unknown> = {
                    base_url: values.base_url,
                    timeout: values.timeout,
                    models: values.models ?? [],
                    enabled: values.enabled,
                };
                if (values.api_key?.trim()) {
                    payload.api_key = values.api_key.trim();
                }
                await client.put(`/config/backends/${encodeURIComponent(editing.name)}`, payload);
                message.success("Provider updated");
            } else {
                await client.post("/config/backends", {
                    name: values.name?.trim(),
                    type: values.type,
                    base_url: values.base_url?.trim(),
                    api_key: values.api_key?.trim() || "",
                    timeout: values.timeout,
                    models: values.models ?? [],
                    enabled: values.enabled,
                });
                message.success("Provider created");
            }
            setModalOpen(false);
            await fetchBackends();
        } catch (error) {
            message.error(errorMessage(error, "Save failed"));
        } finally {
            setSaving(false);
        }
    };

    const toggleEnabled = async (record: Backend, enabled: boolean) => {
        try {
            await client.put(`/config/backends/${encodeURIComponent(record.name)}`, { enabled });
            message.success(enabled ? "Provider enabled" : "Provider disabled");
            await fetchBackends();
        } catch (error) {
            message.error(errorMessage(error, "Failed to update provider"));
        }
    };

    const remove = async (record: Backend) => {
        try {
            await client.delete(`/config/backends/${encodeURIComponent(record.name)}`);
            message.success("Provider deleted");
            await fetchBackends();
        } catch (error) {
            message.error(errorMessage(error, "Failed to delete provider"));
        }
    };

    const columns = [
        { title: "Name", dataIndex: "name", key: "name" },
        {
            title: "Type",
            dataIndex: "type",
            key: "type",
            render: (value: string) => <Tag color="blue">{typeLabel(value)}</Tag>,
        },
        { title: "Base URL", dataIndex: "base_url", key: "base_url", ellipsis: true },
        {
            title: "API Key",
            dataIndex: "api_key",
            key: "api_key",
            render: (value: string) => (value ? <Tag>{value}</Tag> : <Tag color="default">not set</Tag>),
        },
        {
            title: "Models",
            dataIndex: "models",
            key: "models",
            render: (models: string[] | null) => {
                if (!models || models.length === 0) {
                    return <Tag color="default">all</Tag>;
                }
                return (
                    <Space wrap>
                        {models.slice(0, 2).map((model) => <Tag key={model}>{model}</Tag>)}
                        {models.length > 2 && <Tag>+{models.length - 2}</Tag>}
                    </Space>
                );
            },
        },
        { title: "Timeout", dataIndex: "timeout", key: "timeout", width: 100 },
        {
            title: "Enabled",
            dataIndex: "enabled",
            key: "enabled",
            width: 100,
            render: (enabled: boolean, record: Backend) => (
                <Switch checked={enabled} onChange={(checked) => toggleEnabled(record, checked)} />
            ),
        },
        {
            title: "Actions",
            key: "actions",
            width: 150,
            render: (_: unknown, record: Backend) => (
                <Space>
                    <Button type="link" size="small" onClick={() => openEdit(record)}>
                        Edit
                    </Button>
                    <Popconfirm title={`Delete provider ${record.name}?`} onConfirm={() => remove(record)}>
                        <Button type="link" size="small" danger>
                            Delete
                        </Button>
                    </Popconfirm>
                </Space>
            ),
        },
    ];

    return (
        <div data-testid="providers-page">
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <div>
                    <Title level={4} style={{ margin: 0 }}>Model Providers</Title>
                    <Paragraph type="secondary" style={{ margin: 0 }}>
                        Provider endpoints and API keys are stored in this gateway and shared by every connected app.
                        Business apps only hold their gateway API key.
                    </Paragraph>
                </div>
                <Space>
                    <Button icon={<ReloadOutlined />} onClick={fetchBackends}>
                        Refresh
                    </Button>
                    <Button type="primary" icon={<PlusOutlined />} onClick={openCreate} data-testid="provider-add">
                        Add Provider
                    </Button>
                </Space>
            </div>
            <Table
                rowKey="name"
                dataSource={backends}
                columns={columns}
                loading={loading}
                pagination={{ defaultPageSize: 20, showSizeChanger: true, showTotal: (total) => `Total ${total}` }}
            />
            <Modal
                title={editing ? `Edit ${editing.name}` : "Add Provider"}
                open={modalOpen}
                onOk={submit}
                confirmLoading={saving}
                onCancel={() => setModalOpen(false)}
                width={640}
                destroyOnClose
            >
                <Form form={form} layout="vertical">
                    <Form.Item
                        name="name"
                        label="Name"
                        rules={[{ required: true, message: "Name is required" }]}
                    >
                        <Input placeholder="claude-main" disabled={!!editing} />
                    </Form.Item>
                    <Form.Item
                        name="type"
                        label="Provider Type"
                        rules={[{ required: true, message: "Type is required" }]}
                    >
                        <Select
                            disabled={!!editing}
                            options={PROVIDER_TYPES}
                            onChange={(value) => {
                                if (editing) {
                                    return;
                                }
                                const preset = PROVIDER_TYPES.find((item) => item.value === value)?.baseURL ?? "";
                                form.setFieldValue("base_url", preset);
                            }}
                        />
                    </Form.Item>
                    <Form.Item
                        name="base_url"
                        label="Base URL"
                        rules={[{ required: true, message: "Base URL is required" }]}
                    >
                        <Input placeholder="https://api.anthropic.com" />
                    </Form.Item>
                    <Form.Item
                        name="api_key"
                        label="API Key"
                        extra={editing ? "Leave blank to keep the existing key." : "Sent to this gateway only; never to the business app."}
                    >
                        <Input.Password autoComplete="new-password" placeholder={editing ? "••••••••" : "sk-..."} />
                    </Form.Item>
                    <Form.Item name="models" label="Supported Models" extra="Empty means every model advertised by this provider.">
                        <Select mode="tags" placeholder="claude-sonnet-4-20250514" />
                    </Form.Item>
                    <Space size="large">
                        <Form.Item name="timeout" label="Timeout">
                            <Input placeholder="60s" style={{ width: 140 }} />
                        </Form.Item>
                        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Space>
                </Form>
            </Modal>
        </div>
    );
}
