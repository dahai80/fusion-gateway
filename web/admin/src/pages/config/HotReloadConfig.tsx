import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Input, InputNumber, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface HotReloadConfig {
    enabled: boolean;
    watch_path: string;
    debounce: string;
    versioning: boolean;
    breaker_drain_timeout: string;
    breaker_warmup_success: number;
}

export default function HotReloadConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<HotReloadConfig>("/config/hot-reload");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/hot-reload", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Hot Reload Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={4}>
                        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="watch_path" label="Watch Path">
                            <Input />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="debounce" label="Debounce">
                            <Input placeholder="e.g. 500ms" />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="versioning" label="Versioning" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="breaker_drain_timeout" label="Breaker Drain Timeout">
                            <Input placeholder="e.g. 30s" />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="breaker_warmup_success" label="Breaker Warmup Success">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
