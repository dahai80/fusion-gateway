import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface RateLimitConfig { enabled: boolean; global_rpm: number; global_tpm: number; key_enforcement: boolean; }

export default function RateLimitConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try { const { data } = await client.get<RateLimitConfig>("/config/rate-limit"); form.setFieldsValue(data); } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try { await client.put("/config/rate-limit", form.getFieldsValue()); notifier.success(); } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Rate Limit Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={6}><Form.Item name="global_rpm" label="Global RPM"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                    <Col span={6}><Form.Item name="global_tpm" label="Global TPM"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                    <Col span={6}><Form.Item name="key_enforcement" label="Key Enforcement" valuePropName="checked"><Switch /></Form.Item></Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
