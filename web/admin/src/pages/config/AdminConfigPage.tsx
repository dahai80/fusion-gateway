import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Input, InputNumber, Table, Button, Space, Row, Col } from "antd";
import { PlusOutlined, DeleteOutlined } from "@ant-design/icons";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface AdminConfig {
    enabled: boolean;
    listen: string;
    log_max_len: number;
    jwt_secret: string;
    users: Record<string, string>;
}

interface UserEntry {
    key: string;
    username: string;
    password: string;
}

export default function AdminConfigPage() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const [users, setUsers] = useState<UserEntry[]>([]);
    const notifier = useConfigNotifier();

    const toUserEntries = (usersMap: Record<string, string>): UserEntry[] =>
        Object.entries(usersMap || {}).map(([username, password]) => ({ key: username, username, password }));

    const toUsersMap = (entries: UserEntry[]): Record<string, string> => {
        const map: Record<string, string> = {};
        for (const e of entries) { if (e.username) map[e.username] = e.password; }
        return map;
    };

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<AdminConfig>("/config/admin");
            form.setFieldsValue({ enabled: data.enabled, listen: data.listen, log_max_len: data.log_max_len, jwt_secret: data.jwt_secret });
            setUsers(toUserEntries(data.users));
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleAddUser = () => {
        const newKey = `new_${Date.now()}`;
        setUsers([...users, { key: newKey, username: "", password: "" }]);
    };

    const handleDeleteUser = (key: string) => {
        setUsers(users.filter((u) => u.key !== key));
    };

    const handleUserChange = (key: string, field: "username" | "password", value: string) => {
        setUsers(users.map((u) => (u.key === key ? { ...u, [field]: value } : u)));
    };

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            await client.put("/config/admin", { ...values, users: toUsersMap(users) });
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    const columns = [
        { title: "Username", dataIndex: "username", render: (_: string, record: UserEntry) => <Input value={record.username} onChange={(e) => handleUserChange(record.key, "username", e.target.value)} /> },
        { title: "Password", dataIndex: "password", render: (_: string, record: UserEntry) => <Input.Password value={record.password} onChange={(e) => handleUserChange(record.key, "password", e.target.value)} /> },
        { title: "Action", width: 80, render: (_: unknown, record: UserEntry) => <Button danger icon={<DeleteOutlined />} onClick={() => handleDeleteUser(record.key)} /> },
    ];

    return (
        <ConfigPageLayout title="Admin Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={12}><Form.Item name="listen" label="Listen"><Input /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="log_max_len" label="Log Max Length">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="jwt_secret" label="JWT Secret"><Input.Password /></Form.Item></Col>
                </Row>
            </Form>
            <Space style={{ marginBottom: 16 }}>
                <Button type="dashed" icon={<PlusOutlined />} onClick={handleAddUser}>Add User</Button>
            </Space>
            <Table columns={columns} dataSource={users} pagination={false} rowKey="key" size="small" />
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
