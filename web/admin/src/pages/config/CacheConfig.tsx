import { useState, useEffect, useCallback } from "react";
import { Form, InputNumber, Switch, Select, Row, Col, Input } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface CacheRedisConfig { addr: string; password: string; db: number; pool_size: number; }
interface CacheConfig { enabled: boolean; max_entries: number; ttl: string; max_memory_mb: number; backend: string; redis: CacheRedisConfig; warmup_file: string; }

export default function CacheConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try { const { data } = await client.get<CacheConfig>("/config/cache"); form.setFieldsValue(data); } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/cache", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Cache Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={8}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={8}><Form.Item name="backend" label="Backend"><Select options={[{ value: "memory" }, { value: "redis" }]} /></Form.Item></Col>
                    <Col span={8}><Form.Item name="warmup_file" label="Warmup File"><Input /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={8}><Form.Item name="max_entries" label="Max Entries"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                    <Col span={8}><Form.Item name="ttl" label="TTL (e.g. 5m)"><Input /></Form.Item></Col>
                    <Col span={8}><Form.Item name="max_memory_mb" label="Max Memory (MB)"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                </Row>
                <Form.Item label="Redis Configuration">
                    <Row gutter={16}>
                        <Col span={8}><Form.Item name={["redis", "addr"]} label="Address"><Input /></Form.Item></Col>
                        <Col span={8}><Form.Item name={["redis", "password"]} label="Password"><Input.Password /></Form.Item></Col>
                        <Col span={4}><Form.Item name={["redis", "db"]} label="DB"><InputNumber min={0} style={{ width: "100%" }} /></Form.Item></Col>
                        <Col span={4}><Form.Item name={["redis", "pool_size"]} label="Pool Size"><InputNumber min={1} style={{ width: "100%" }} /></Form.Item></Col>
                    </Row>
                </Form.Item>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
