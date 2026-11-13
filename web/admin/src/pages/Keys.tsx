import { useState, useEffect } from "react";
import { Table, Button, Modal, Form, Input, Select, Tag, Space, message, Popconfirm, Typography, theme } from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import client from "../api/client";

const { Title } = Typography;

interface ApiKey {
    id: number;
    key: string;
    name: string;
    status: number;
    models: string[];
    quota: number;
    used_quota: number;
    created_at: string;
}

interface KeyForm {
    name: string;
    status: number;
    models: string[];
    quota: number;
}

export default function Keys() {
    const { token } = theme.useToken();
    const [keys, setKeys] = useState<ApiKey[]>([]);
    const [loading, setLoading] = useState(false);
    const [modalOpen, setModalOpen] = useState(false);
    const [editId, setEditId] = useState<number | null>(null);
    const [form] = Form.useForm<KeyForm>();
    const [createdKey, setCreatedKey] = useState<string | null>(null);

    const fetchKeys = async () => {
        setLoading(true);
        try {
            const res = await client.get("/keys");
            setKeys(res.data?.data || res.data || []);
        } catch (err) {
            message.error("Failed to fetch keys");
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        fetchKeys();
    }, []);

    const handleCreate = () => {
        setEditId(null);
        form.resetFields();
        setModalOpen(true);
    };

    const handleEdit = (record: ApiKey) => {
        setEditId(record.id);
        form.setFieldsValue({
            name: record.name,
            status: record.status,
            models: record.models,
            quota: record.quota,
        });
        setModalOpen(true);
    };

    const handleDelete = async (id: number) => {
        try {
            await client.delete(`/keys/${id}`);
            message.success("Key deleted (disabled, kept for audit)");
            fetchKeys();
        } catch {
            message.error("Failed to delete key");
        }
    };

    // Purge = irreversible hard delete (backend ?purge=true). The record,
    // its hash and quota history are gone for good, so the confirmation
    // requires typing the exact key name.
    const handlePurge = (record: ApiKey) => {
        let typed = "";
        Modal.confirm({
            title: "Purge key permanently?",
            content: (
                <div>
                    <p style={{ marginTop: 8 }}>
                        This irreversibly removes <Typography.Text code>{record.name}</Typography.Text> and its quota
                        history. A normal Delete keeps the record (disabled) for audit.
                    </p>
                    <Input
                        placeholder={`Type "${record.name}" to confirm`}
                        onChange={(e) => {
                            typed = e.target.value;
                        }}
                    />
                </div>
            ),
            okText: "Purge",
            okButtonProps: { danger: true },
            onOk: async () => {
                if (typed !== record.name) {
                    message.error("Key name does not match");
                    return Promise.reject();
                }
                try {
                    await client.delete(`/keys/${record.id}?purge=true`);
                    message.success("Key purged");
                    fetchKeys();
                } catch {
                    message.error("Failed to purge key");
                    return Promise.reject();
                }
            },
        });
    };

    const handleSubmit = async () => {
        try {
            const values = await form.validateFields();
            if (editId) {
                await client.put(`/keys/${editId}`, values);
                message.success("Key updated");
            } else {
                const res = await client.post("/keys", values);
                const rawKey = res.data?.raw_key || res.data?.key || null;
                if (rawKey) {
                    setCreatedKey(rawKey);
                }
                message.success("Key created");
            }
            setModalOpen(false);
            fetchKeys();
        } catch {
            message.error("Operation failed");
        }
    };

    const columns = [
        { title: "ID", dataIndex: "id", key: "id", width: 60 },
        { title: "Name", dataIndex: "name", key: "name" },
        {
            title: "Key",
            dataIndex: "key",
            key: "key",
            render: (k: string) => (
                <Typography.Text copyable code>
                    {k.length > 16 ? `${k.slice(0, 8)}...${k.slice(-4)}` : k}
                </Typography.Text>
            ),
        },
        {
            title: "Status",
            dataIndex: "status",
            key: "status",
            render: (s: number) => (
                <Tag color={s === 1 ? "green" : "red"}>{s === 1 ? "Active" : "Disabled"}</Tag>
            ),
        },
        {
            title: "Models",
            dataIndex: "models",
            key: "models",
            render: (models: string[]) => models?.map((m) => <Tag key={m}>{m}</Tag>),
        },
        {
            title: "Quota",
            key: "quota",
            render: (_: unknown, r: ApiKey) => `${r.used_quota} / ${r.quota || "Unlimited"}`,
        },
        { title: "Created", dataIndex: "created_at", key: "created_at", width: 160 },
        {
            title: "Actions",
            key: "actions",
            width: 190,
            render: (_: unknown, record: ApiKey) => (
                <Space>
                    <Button type="link" size="small" onClick={() => handleEdit(record)}>
                        Edit
                    </Button>
                    <Popconfirm title="Delete this key?" onConfirm={() => handleDelete(record.id)}>
                        <Button type="link" size="small" danger>
                            Delete
                        </Button>
                    </Popconfirm>
                    <Button type="link" size="small" danger onClick={() => handlePurge(record)}>
                        Purge
                    </Button>
                </Space>
            ),
        },
    ];

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16 }}>
                <Title level={4} style={{ margin: 0 }}>API Keys</Title>
                <Space>
                    <Button icon={<ReloadOutlined />} onClick={fetchKeys}>
                        Refresh
                    </Button>
                    <Button type="primary" icon={<PlusOutlined />} onClick={handleCreate}>
                        Create Key
                    </Button>
                </Space>
            </div>
            <Table
                rowKey="id"
                columns={columns}
                dataSource={keys}
                loading={loading}
                pagination={{ defaultPageSize: 20, showSizeChanger: true, showTotal: (t) => `Total ${t}` }}
            />
            <Modal
                title={editId ? "Edit Key" : "Create Key"}
                open={modalOpen}
                onOk={handleSubmit}
                onCancel={() => setModalOpen(false)}
                destroyOnClose
            >
                <Form form={form} layout="vertical" initialValues={{ status: 1, quota: 0, models: [] }}>
                    <Form.Item name="name" label="Name" rules={[{ required: true, message: "Required" }]}>
                        <Input placeholder="Key name" />
                    </Form.Item>
                    <Form.Item name="status" label="Status" rules={[{ required: true }]}>
                        <Select
                            options={[
                                { label: "Active", value: 1 },
                                { label: "Disabled", value: 0 },
                            ]}
                        />
                    </Form.Item>
                    <Form.Item name="models" label="Allowed Models">
                        <Select mode="tags" placeholder="Leave empty for all models" />
                    </Form.Item>
                    <Form.Item name="quota" label="Quota (0 = unlimited)">
                        <Input type="number" min={0} />
                    </Form.Item>
                </Form>
            </Modal>
            <Modal
                title="API Key Created"
                open={!!createdKey}
                onOk={() => setCreatedKey(null)}
                onCancel={() => setCreatedKey(null)}
                okText="I've saved the key"
                closable={false}
            >
                <p style={{ marginBottom: 8, color: token.colorWarning }}>
                    Copy the key now. You won't be able to see it again!
                </p>
                <Typography.Text copyable code style={{ fontSize: 13, wordBreak: "break-all" }}>
                    {createdKey}
                </Typography.Text>
            </Modal>
        </div>
    );
}
