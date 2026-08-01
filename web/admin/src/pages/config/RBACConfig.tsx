import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Select, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface RBACConfig {
    enabled: boolean;
    default_role: string;
    team_enabled: boolean;
    default_team: string;
}

export default function RBACConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<RBACConfig>("/config/rbac");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/rbac", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="RBAC Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="default_role" label="Default Role">
                            <Select options={[
                                { value: "admin", label: "Admin" },
                                { value: "user", label: "User" },
                                { value: "viewer", label: "Viewer" },
                            ]} />
                        </Form.Item>
                    </Col>
                    <Col span={6}><Form.Item name="team_enabled" label="Team Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="default_team" label="Default Team">
                            <Select options={[
                                { value: "engineering", label: "Engineering" },
                                { value: "product", label: "Product" },
                                { value: "operations", label: "Operations" },
                            ]} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
