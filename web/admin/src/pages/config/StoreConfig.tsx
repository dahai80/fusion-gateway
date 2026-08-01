import { useState, useEffect, useCallback } from "react";
import { Form, Select, Input, InputNumber, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface StoreConfig {
    backend: string;
    redis_addr: string;
    redis_password: string;
    redis_db: number;
    redis_pool_size: number;
}

export default function StoreConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<StoreConfig>("/config/store");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/store", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="Store Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="backend" label="Backend">
                            <Select options={[
                                { value: "memory", label: "Memory" },
                                { value: "redis", label: "Redis" },
                            ]} />
                        </Form.Item>
                    </Col>
                    <Col span={16}><Form.Item name="redis_addr" label="Redis Address"><Input /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={8}><Form.Item name="redis_password" label="Redis Password"><Input.Password /></Form.Item></Col>
                    <Col span={8}>
                        <Form.Item name="redis_db" label="Redis DB">
                            <InputNumber min={0} max={15} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="redis_pool_size" label="Redis Pool Size">
                            <InputNumber min={1} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
