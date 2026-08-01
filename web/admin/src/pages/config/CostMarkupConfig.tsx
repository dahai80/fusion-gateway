import { useState, useEffect, useCallback } from "react";
import { Form, Switch, InputNumber, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface CostMarkupConfig {
    enabled: boolean;
    global_markup: number;
}

export default function CostMarkupConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<CostMarkupConfig>("/config/cost-markup");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/cost-markup", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Cost Markup Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={8}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={8}>
                        <Form.Item name="global_markup" label="Global Markup">
                            <InputNumber min={0} step={0.01} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
