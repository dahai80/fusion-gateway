import { useState, useEffect, useCallback } from "react";
import { Form, Switch, InputNumber, Input, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface SemanticCacheConfig {
    enabled: boolean;
    similarity_threshold: number;
    max_entries: number;
    provider: string;
    endpoint: string;
}

export default function SemanticCacheConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<SemanticCacheConfig>("/config/semantic-cache");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/semantic-cache", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Semantic Cache Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="similarity_threshold" label="Similarity Threshold">
                            <InputNumber min={0} max={1} step={0.01} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="max_entries" label="Max Entries">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="provider" label="Provider"><Input /></Form.Item></Col>
                    <Col span={12}><Form.Item name="endpoint" label="Endpoint"><Input /></Form.Item></Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
