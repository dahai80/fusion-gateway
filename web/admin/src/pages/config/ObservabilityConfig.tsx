import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Select, Input, Row, Col, Divider } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface ObservabilityConfig {
    log_format: string;
    log_file: string;
    log_rotation_max_size: number;
    log_rotation_max_backups: number;
    metrics_enabled: boolean;
    metrics_path: string;
    audit_log_enabled: boolean;
    config_audit_log: boolean;
    config_audit_file: string;
    otel_enabled: boolean;
    otel_endpoint: string;
    otel_protocol: string;
    otel_service_name: string;
}

export default function ObservabilityConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<ObservabilityConfig>("/config/observability");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/observability", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Observability Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Divider orientation="left">Logging</Divider>
                <Row gutter={16}>
                    <Col span={6}>
                        <Form.Item name="log_format" label="Log Format">
                            <Select options={[{ value: "json" }, { value: "text" }]} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="log_file" label="Log File">
                            <Input />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="log_rotation_max_size" label="Rotation Max Size (MB)">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="log_rotation_max_backups" label="Rotation Max Backups">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>

                <Divider orientation="left">Metrics</Divider>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="metrics_enabled" label="Metrics Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item name="metrics_path" label="Metrics Path">
                            <Input />
                        </Form.Item>
                    </Col>
                </Row>

                <Divider orientation="left">Audit</Divider>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="audit_log_enabled" label="Audit Log Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="config_audit_log" label="Config Audit Log" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="config_audit_file" label="Config Audit File">
                            <Input />
                        </Form.Item>
                    </Col>
                </Row>

                <Divider orientation="left">OpenTelemetry</Divider>
                <Row gutter={16}>
                    <Col span={6}>
                        <Form.Item name="otel_enabled" label="OTel Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="otel_endpoint" label="OTel Endpoint">
                            <Input />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="otel_protocol" label="OTel Protocol">
                            <Select options={[{ value: "grpc" }, { value: "http" }]} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name="otel_service_name" label="Service Name">
                            <Input />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
