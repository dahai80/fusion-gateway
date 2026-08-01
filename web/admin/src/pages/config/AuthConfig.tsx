import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Row, Col, Input, Table, Modal, Button, Space, Select } from "antd";
import { PlusOutlined, DeleteOutlined } from "@ant-design/icons";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface AuthKey {
    key: string;
    name: string;
    rpm: number;
    tpm: number;
    allowed_models: string[];
    allowed_backends: string[];
    expires_at: string;
    budget_limit: number;
    metadata: Record<string, string>;
}

interface AuthConfig {
    enabled: boolean;
    master_key: string;
    passthrough: boolean;
    api_keys: AuthKey[];
}

const defaultAuthKey: AuthKey = {
    key: "",
    name: "",
    rpm: 0,
    tpm: 0,
    allowed_models: [],
    allowed_backends: [],
    expires_at: "",
    budget_limit: 0,
    metadata: {},
};

function maskKey(key: string): string {
    if (!key || key.length <= 8) return key ? "****" : "";
    return key.slice(0, 4) + "****" + key.slice(-4);
}

export default function AuthConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const [apiKeys, setApiKeys] = useState<AuthKey[]>([]);
    const [modalOpen, setModalOpen] = useState(false);
    const [modalForm] = Form.useForm();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<AuthConfig>("/config/auth");
            form.setFieldsValue({
                enabled: data.enabled,
                master_key: data.master_key,
                passthrough: data.passthrough,
            });
            setApiKeys(data.api_keys || []);
        } catch {
            notifier.error("Failed to load");
        }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            await client.put("/config/auth", {
                ...values,
                api_keys: apiKeys,
            });
            notifier.success();
        } catch {
            notifier.error("Failed to save");
        }
        setSaving(false);
    };

    const handleAddKey = () => {
        modalForm.resetFields();
        setModalOpen(true);
    };

    const handleModalOk = async () => {
        try { await modalForm.validateFields(); } catch { return; }
        const values = modalForm.getFieldsValue();
        const newKey: AuthKey = {
            ...defaultAuthKey,
            ...values,
            allowed_models: values.allowed_models || [],
            allowed_backends: values.allowed_backends || [],
            metadata: values.metadata || {},
        };
        if (!newKey.key) {
            newKey.key = "fg-" + Math.random().toString(36).slice(2, 10) + Math.random().toString(36).slice(2, 10);
        }
        setApiKeys((prev) => [...prev, newKey]);
        setModalOpen(false);
        modalForm.resetFields();
    };

    const handleDeleteKey = (index: number) => {
        setApiKeys((prev) => prev.filter((_, i) => i !== index));
    };

    const columns = [
        {
            title: "Name",
            dataIndex: "name",
            key: "name",
            width: 160,
        },
        {
            title: "Key",
            dataIndex: "key",
            key: "key",
            width: 180,
            render: (key: string) => maskKey(key),
        },
        {
            title: "RPM",
            dataIndex: "rpm",
            key: "rpm",
            width: 80,
        },
        {
            title: "TPM",
            dataIndex: "tpm",
            key: "tpm",
            width: 80,
        },
        {
            title: "Actions",
            key: "actions",
            width: 80,
            render: (_: unknown, __: AuthKey, index: number) => (
                <Button
                    type="text"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => handleDeleteKey(index)}
                />
            ),
        },
    ];

    return (
        <ConfigPageLayout title="Auth Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="passthrough" label="Passthrough" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="master_key" label="Master Key">
                            <Input.Password />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>

            <div style={{ marginBottom: 16 }}>
                <Space style={{ marginBottom: 12 }}>
                    <Button type="primary" icon={<PlusOutlined />} onClick={handleAddKey}>
                        Add API Key
                    </Button>
                </Space>
                <Table
                    rowKey={(_, index) => String(index)}
                    columns={columns}
                    dataSource={apiKeys}
                    pagination={false}
                    size="small"
                />
            </div>

            <Modal
                title="Add API Key"
                open={modalOpen}
                onOk={handleModalOk}
                onCancel={() => setModalOpen(false)}
                okText="Add"
                destroyOnClose
            >
                <Form form={modalForm} layout="vertical">
                    <Form.Item name="name" label="Name" rules={[{ required: true, message: "Name is required" }]}>
                        <Input />
                    </Form.Item>
                    <Form.Item name="key" label="Key (leave empty to auto-generate)">
                        <Input placeholder="Auto-generated if empty" />
                    </Form.Item>
                    <Row gutter={16}>
                        <Col span={12}>
                            <Form.Item name="rpm" label="RPM" initialValue={0}>
                                <InputNumber min={0} style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                        <Col span={12}>
                            <Form.Item name="tpm" label="TPM" initialValue={0}>
                                <InputNumber min={0} style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                    </Row>
                    <Form.Item name="allowed_models" label="Allowed Models">
                        <Select mode="tags" tokenSeparators={[","]} style={{ width: "100%" }} placeholder="e.g. qwen3, llama3" />
                    </Form.Item>
                    <Form.Item name="allowed_backends" label="Allowed Backends">
                        <Select mode="tags" tokenSeparators={[","]} style={{ width: "100%" }} placeholder="e.g. local, cloud" />
                    </Form.Item>
                    <Row gutter={16}>
                        <Col span={12}>
                            <Form.Item name="expires_at" label="Expires At">
                                <Input placeholder="e.g. 2026-12-31" />
                            </Form.Item>
                        </Col>
                        <Col span={12}>
                            <Form.Item name="budget_limit" label="Budget Limit" initialValue={0}>
                                <InputNumber min={0} step={0.01} style={{ width: "100%" }} />
                            </Form.Item>
                        </Col>
                    </Row>
                </Form>
            </Modal>

            {notifier.ctx}
        </ConfigPageLayout>
    );
}
