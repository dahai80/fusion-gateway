import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Input, Row, Col, Divider } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface TokenizerConfig {
    provider: string;
    default_max_tokens_strategy: string;
    context_window_ratio: number;
    min_max_tokens: number;
    vision_token_estimate: number;
    scene_presets_chat: number;
    scene_presets_code: number;
    scene_presets_tool_call: number;
    calibration_enabled: boolean;
    calibration_sample_interval: number;
    calibration_sample_size: number;
    calibration_deviation_threshold: number;
    calibration_auto_switch_threshold: number;
}

export default function TokenizerConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<TokenizerConfig>("/config/tokenizer");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/tokenizer", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Tokenizer Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Divider orientation="left">General</Divider>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="provider" label="Provider">
                            <Input />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="default_max_tokens_strategy" label="Default Max Tokens Strategy">
                            <Input />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="context_window_ratio" label="Context Window Ratio">
                            <InputNumber min={0} max={1} step={0.1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="min_max_tokens" label="Min Max Tokens">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="vision_token_estimate" label="Vision Token Estimate">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>

                <Divider orientation="left">Scene Presets</Divider>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="scene_presets_chat" label="Chat">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="scene_presets_code" label="Code">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="scene_presets_tool_call" label="Tool Call">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>

                <Divider orientation="left">Calibration</Divider>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="calibration_enabled" label="Calibration Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="calibration_sample_interval" label="Sample Interval">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="calibration_sample_size" label="Sample Size">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="calibration_deviation_threshold" label="Deviation Threshold">
                            <InputNumber min={0} step={0.01} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item name="calibration_auto_switch_threshold" label="Auto Switch Threshold">
                            <InputNumber min={0} step={0.01} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
