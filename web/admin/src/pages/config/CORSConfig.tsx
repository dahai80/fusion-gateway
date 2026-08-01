import { useState, useEffect, useCallback } from "react";
import { Form, Select, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface CORSConfig { allowed_origins: string[]; allowed_methods: string[]; allowed_headers: string[]; }

export default function CORSConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try { const { data } = await client.get<CORSConfig>("/config/cors"); form.setFieldsValue(data); } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/cors", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="CORS Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="allowed_origins" label="Allowed Origins">
                            <Select mode="tags" tokenSeparators={[","]} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="allowed_methods" label="Allowed Methods">
                            <Select mode="tags" tokenSeparators={[","]} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="allowed_headers" label="Allowed Headers">
                            <Select mode="tags" tokenSeparators={[","]} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
