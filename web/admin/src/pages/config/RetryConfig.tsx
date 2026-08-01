import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Row, Col, Input, Select } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface RetryConfig { max_retries: number; initial_backoff: string; max_backoff: string; retryable_status_codes: number[]; }

export default function RetryConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try { const { data } = await client.get<RetryConfig>("/config/retry"); form.setFieldsValue(data); } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try { await client.put("/config/retry", form.getFieldsValue()); notifier.success(); } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Retry Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="max_retries" label="Max Retries"><InputNumber min={0} max={10} style={{ width: "100%" }} /></Form.Item></Col>
                    <Col span={6}><Form.Item name="initial_backoff" label="Initial Backoff"><Input placeholder="e.g. 1s" /></Form.Item></Col>
                    <Col span={6}><Form.Item name="max_backoff" label="Max Backoff"><Input placeholder="e.g. 30s" /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="retryable_status_codes" label="Retryable Status Codes">
                            <Select mode="tags" placeholder="e.g. 429, 500, 502, 503" tokenSeparators={[","]} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
