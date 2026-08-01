import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Select, Row, Col, Input } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface ServerConfig {
    host: string;
    port: number;
    log_level: string;
    graceful_shutdown_timeout: number;
    max_request_body_size: number;
    enable_pprof: boolean;
}

export default function ServerConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<ServerConfig>("/config/server");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load config"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            const patch: Record<string, unknown> = {};
            for (const [k, v] of Object.entries(values)) { if (v !== undefined && v !== null) patch[k] = v; }
            await client.put("/config/server", patch);
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Server Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="host" label="Host"><Input /></Form.Item></Col>
                    <Col span={12}><Form.Item name="port" label="Port"><InputNumber min={1} max={65535} style={{ width: "100%" }} /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}>
                        <Form.Item name="log_level" label="Log Level">
                            <Select options={[{ value: "debug" }, { value: "info" }, { value: "warn" }, { value: "error" }]} />
                        </Form.Item>
                    </Col>
                    <Col span={12}><Form.Item name="graceful_shutdown_timeout" label="Shutdown Timeout (s)"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="max_request_body_size" label="Max Request Body Size (bytes)"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                    <Col span={12}><Form.Item name="enable_pprof" label="Enable PProf" valuePropName="checked"><Switch /></Form.Item></Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
