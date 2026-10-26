import { useState, useEffect, useCallback } from "react";
import { Form, Switch, InputNumber, Input, Row, Col, Typography, Alert } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

const { Text } = Typography;

interface PersistenceConfig {
    enabled: boolean;
    postgres: {
        dsn_env: string;
        max_open_conns: number;
        max_idle_conns: number;
        conn_timeout_secs: number;
    };
    queue: {
        capacity: number;
        batch_size: number;
        flush_interval_ms: number;
    };
    capture: {
        request_body: boolean;
        response_body: boolean;
        max_body_bytes: number;
        strip_auth_fields: boolean;
        image_placeholder: boolean;
        stream_chunks: boolean;
    };
    retention: {
        request_logs_days: number;
        request_payloads_days: number;
        hw_samples_days: number;
        gateway_events_days: number;
        insights_days: number;
        stream_chunks_days: number;
    };
}

export default function PersistenceConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<PersistenceConfig>("/config/persistence");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/persistence", form.getFieldsValue(true));
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Persistence Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Alert
                type="info"
                showIcon
                style={{ marginBottom: 24 }}
                message="Changes take effect after a gateway restart"
                description="The PostgreSQL pool and capture pipeline are built at startup. The DSN itself never lives in config.yaml — postgres.dsn_env names the environment variable carrying it (default FUSION_GW_PG_DSN)."
            />
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}>
                        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                </Row>
                <Text strong>PostgreSQL</Text>
                <Row gutter={16} style={{ marginTop: 8 }}>
                    <Col span={6}>
                        <Form.Item name={["postgres", "dsn_env"]} label="DSN Env Var">
                            <Input placeholder="FUSION_GW_PG_DSN" />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["postgres", "max_open_conns"]} label="Max Open Conns">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["postgres", "max_idle_conns"]} label="Max Idle Conns">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["postgres", "conn_timeout_secs"]} label="Conn Timeout (s)">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
                <Text strong>Write Queue</Text>
                <Row gutter={16} style={{ marginTop: 8 }}>
                    <Col span={6}>
                        <Form.Item name={["queue", "capacity"]} label="Capacity">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["queue", "batch_size"]} label="Batch Size">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["queue", "flush_interval_ms"]} label="Flush Interval (ms)">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
                <Text strong>Body Capture</Text>
                <Row gutter={16} style={{ marginTop: 8 }}>
                    <Col span={6}>
                        <Form.Item name={["capture", "request_body"]} label="Capture Request Body" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["capture", "response_body"]} label="Capture Response Body" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["capture", "max_body_bytes"]} label="Max Body Bytes">
                            <InputNumber min={1024} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={6}>
                        <Form.Item name={["capture", "strip_auth_fields"]} label="Strip Auth Fields" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item
                            name={["capture", "image_placeholder"]}
                            label="Image Placeholder"
                            valuePropName="checked"
                            tooltip="Swap inline base64 image payloads for a <image:NNNB> placeholder before persist — multimodal bodies otherwise dominate storage."
                        >
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={12}>
                        <Form.Item
                            name={["capture", "stream_chunks"]}
                            label="Stream Chunk Timing Capture"
                            valuePropName="checked"
                            tooltip="Persist per-chunk stream timings (seq / delta / bytes) to fg_stream_chunks for Explorer timing forensics. High volume — keep off unless debugging stream pacing."
                        >
                            <Switch />
                        </Form.Item>
                    </Col>
                </Row>
                <Text strong>Retention (days, partition DROP)</Text>
                <Row gutter={16} style={{ marginTop: 8 }}>
                    <Col span={4}>
                        <Form.Item name={["retention", "request_logs_days"]} label="Request Logs">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name={["retention", "request_payloads_days"]} label="Request Payloads">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name={["retention", "stream_chunks_days"]} label="Stream Chunks">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name={["retention", "hw_samples_days"]} label="HW Samples">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name={["retention", "gateway_events_days"]} label="Gateway Events">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name={["retention", "insights_days"]} label="Insights (anomalies/findings/reports)">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
