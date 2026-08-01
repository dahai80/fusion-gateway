import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Select, Input, InputNumber, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface PromptInjectionConfig {
    enabled: boolean;
    action: string;
    provider: string;
    api_key: string;
    threshold: number;
}

export default function PromptInjectionConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<PromptInjectionConfig>("/config/prompt-injection");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/prompt-injection", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Prompt Injection Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="action" label="Action">
                            <Select options={[
                                { value: "block", label: "Block" },
                                { value: "log", label: "Log" },
                                { value: "tag", label: "Tag" },
                            ]} />
                        </Form.Item>
                    </Col>
                    <Col span={12}><Form.Item name="provider" label="Provider"><Input /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="api_key" label="API Key"><Input.Password /></Form.Item></Col>
                    <Col span={6}>
                        <Form.Item name="threshold" label="Threshold">
                            <InputNumber min={0} max={1} step={0.01} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
