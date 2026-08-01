import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Row, Col, Input } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface HardwareConfig {
    enabled: boolean;
    collect_interval: string;
    iokit_enabled: boolean;
    gopsutil_enabled: boolean;
    mlx_metrics_enabled: boolean;
    mlx_metrics_interval: string;
    swap_page_rate_sampling: boolean;
    swap_page_rate_threshold: number;
    collection_error_protection: boolean;
}

export default function HardwareConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<HardwareConfig>("/config/hardware");
            form.setFieldsValue(data);
        } catch {
            notifier.error("Failed to load config");
        }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            const patch: Record<string, unknown> = {};
            for (const [k, v] of Object.entries(values)) {
                if (v !== undefined && v !== null) patch[k] = v;
            }
            await client.put("/config/hardware", patch);
            notifier.success();
        } catch {
            notifier.error("Failed to save");
        }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Hardware Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item name="collect_interval" label="Collect Interval">
                            <Input placeholder="e.g. 5s, 1m" />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="collection_error_protection" label="Collection Error Protection" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="iokit_enabled" label="IOKit Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item name="gopsutil_enabled" label="Gopsutil Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="mlx_metrics_enabled" label="MLX Metrics Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item name="mlx_metrics_interval" label="MLX Metrics Interval">
                            <Input placeholder="e.g. 10s, 1m" />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="swap_page_rate_sampling" label="Swap Page Rate Sampling" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item name="swap_page_rate_threshold" label="Swap Page Rate Threshold">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
