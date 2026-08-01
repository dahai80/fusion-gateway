import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Row, Col, Input } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface CostConfig { enabled: boolean; pricing_file: string; budget_alert_threshold: number; }

export default function CostConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try { const { data } = await client.get<CostConfig>("/config/cost"); form.setFieldsValue(data); } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try { await client.put("/config/cost", form.getFieldsValue()); notifier.success(); } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Cost Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={8}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={8}><Form.Item name="pricing_file" label="Pricing File"><Input /></Form.Item></Col>
                    <Col span={8}><Form.Item name="budget_alert_threshold" label="Budget Alert Threshold"><InputNumber min={0} step={0.1} style={{ width: "100%" }} /></Form.Item></Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
