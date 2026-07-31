import { useState, useEffect } from "react";
import { Table, Button, Modal, Form, Input, Select, InputNumber, Tag, Space, message, Popconfirm, Typography, Switch } from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import client from "../api/client";

const { Title } = Typography;

interface Channel {
    id: number;
    name: string;
    type: string;
    base_url: string;
    models: string[];
    status: number;
    priority: number;
    weight: number;
    max_tokens: number;
    created_at: string;
}

interface ChannelForm {
    name: string;
    type: string;
    base_url: string;
    key: string;
    models: string[];
    status: number;
    priority: number;
    weight: number;
    max_tokens: number;
}

const CHANNEL_TYPES = [
    { label: "Fusion-MLX (Local)", value: "fusion_mlx" },
    { label: "OpenAI", value: "openai" },
    { label: "Claude", value: "claude" },
    { label: "Volcengine", value: "volcengine" },
    { label: "Qianfan", value: "qianfan" },
    { label: "vLLM-Ascend", value: "vllm_ascend" },
    { label: "vLLM-CUDA", value: "vllm_cuda" },
    { label: "llama.cpp", value: "llamacpp" },
];

export default function Channels() {
    const [channels, setChannels] = useState<Channel[]>([]);
    const [loading, setLoading] = useState(false);
    const [modalOpen, setModalOpen] = useState(false);
    const [editId, setEditId] = useState<number | null>(null);
    const [form] = Form.useForm<ChannelForm>();

    const fetchChannels = async () => {
        setLoading(true);
        try {
            const res = await client.get("/channels");
            setChannels(res.data?.data || res.data || []);
        } catch {
            message.error("Failed to fetch channels");
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        fetchChannels();
    }, []);

    const handleCreate = () => {
        setEditId(null);
        form.resetFields();
        setModalOpen(true);
    };

    const handleEdit = (record: Channel) => {
        setEditId(record.id);
        form.setFieldsValue({
            name: record.name,
            type: record.type,
            base_url: record.base_url,
            models: record.models,
            status: record.status,
            priority: record.priority,
            weight: record.weight,
            max_tokens: record.max_tokens,
        });
        setModalOpen(true);
    };

    const handleDelete = async (id: number) => {
        try {
            await client.delete(`/channels/${id}`);
            message.success("Channel deleted");
            fetchChannels();
        } catch {
            message.error("Failed to delete channel");
        }
    };

    const handleToggleStatus = async (record: Channel) => {
        try {
            await client.put(`/channels/${record.id}`, {
                ...record,
                status: record.status === 1 ? 0 : 1,
            });
            message.success("Status updated");
            fetchChannels();
        } catch {
            message.error("Failed to update status");
        }
    };

    const handleSubmit = async () => {
        try {
            const values = await form.validateFields();
            if (editId) {
                await client.put(`/channels/${editId}`, values);
                message.success("Channel updated");
            } else {
                await client.post("/channels", values);
                message.success("Channel created");
            }
            setModalOpen(false);
            fetchChannels();
        } catch {
            message.error("Operation failed");
        }
    };

    const columns = [
        { title: "ID", dataIndex: "id", key: "id", width: 60 },
        { title: "Name", dataIndex: "name", key: "name" },
        {
            title: "Type",
            dataIndex: "type",
            key: "type",
            render: (t: string) => {
                const found = CHANNEL_TYPES.find((c) => c.value === t);
                return <Tag color="blue">{found?.label || t}</Tag>;
            },
        },
        { title: "Base URL", dataIndex: "base_url", key: "base_url", ellipsis: true },
        {
            title: "Models",
            dataIndex: "models",
            key: "models",
            render: (models: string[]) => (
                <Space wrap>
                    {models?.slice(0, 3).map((m) => <Tag key={m}>{m}</Tag>)}
                    {models?.length > 3 && <Tag>+{models.length - 3}</Tag>}
                </Space>
            ),
        },
        {
            title: "Status",
            dataIndex: "status",
            key: "status",
            render: (s: number, record: Channel) => (
                <Switch
                    checked={s === 1}
                    onChange={() => handleToggleStatus(record)}
                    checkedChildren="On"
                    unCheckedChildren="Off"
                />
            ),
        },
        { title: "Priority", dataIndex: "priority", key: "priority", width: 80 },
        { title: "Weight", dataIndex: "weight", key: "weight", width: 80 },
        { title: "Created", dataIndex: "created_at", key: "created_at", width: 160 },
        {
            title: "Actions",
            key: "actions",
            width: 150,
            render: (_: unknown, record: Channel) => (
                <Space>
                    <Button type="link" size="small" onClick={() => handleEdit(record)}>
                        Edit
                    </Button>
                    <Popconfirm title="Delete this channel?" onConfirm={() => handleDelete(record.id)}>
                        <Button type="link" size="small" danger>
                            Delete
                        </Button>
                    </Popconfirm>
                </Space>
            ),
        },
    ];

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>Channels</Title>
                <Space>
                    <Button icon={<ReloadOutlined />} onClick={fetchChannels}>
                        Refresh
                    </Button>
                    <Button type="primary" icon={<PlusOutlined />} onClick={handleCreate}>
                        Add Channel
                    </Button>
                </Space>
            </div>
            <Table
                rowKey="id"
                columns={columns}
                dataSource={channels}
                loading={loading}
                pagination={{ pageSize: 20, showSizeChanger: true, showTotal: (t) => `Total ${t}` }}
            />
            <Modal
                title={editId ? "Edit Channel" : "Add Channel"}
                open={modalOpen}
                onOk={handleSubmit}
                onCancel={() => setModalOpen(false)}
                width={600}
                destroyOnClose
            >
                <Form form={form} layout="vertical" initialValues={{ status: 1, priority: 0, weight: 1, max_tokens: 4096, models: [] }}>
                    <Form.Item name="name" label="Name" rules={[{ required: true, message: "Required" }]}>
                        <Input placeholder="Channel name" />
                    </Form.Item>
                    <Form.Item name="type" label="Type" rules={[{ required: true, message: "Required" }]}>
                        <Select options={CHANNEL_TYPES} placeholder="Select type" />
                    </Form.Item>
                    <Form.Item name="base_url" label="Base URL" rules={[{ required: true, message: "Required" }]}>
                        <Input placeholder="https://api.example.com/v1" />
                    </Form.Item>
                    <Form.Item name="key" label="API Key">
                        <Input.Password placeholder="Leave empty if not needed" />
                    </Form.Item>
                    <Form.Item name="models" label="Supported Models">
                        <Select mode="tags" placeholder="Enter model names" />
                    </Form.Item>
                    <Space style={{ width: "100%" }} size="large">
                        <Form.Item name="priority" label="Priority">
                            <InputNumber min={0} max={100} />
                        </Form.Item>
                        <Form.Item name="weight" label="Weight">
                            <InputNumber min={1} max={100} />
                        </Form.Item>
                        <Form.Item name="max_tokens" label="Max Tokens">
                            <InputNumber min={1} max={200000} />
                        </Form.Item>
                    </Space>
                    <Form.Item name="status" label="Status">
                        <Select options={[{ label: "Enabled", value: 1 }, { label: "Disabled", value: 0 }]} />
                    </Form.Item>
                </Form>
            </Modal>
        </div>
    );
}
