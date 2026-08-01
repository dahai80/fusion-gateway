import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Input, Row, Col } from "antd";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface OIDCConfig {
    enabled: boolean;
    issuer: string;
    client_id: string;
    audiences: string;
    scopes: string;
    claim_mappings: string;
}

export default function OIDCConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<OIDCConfig>("/config/oidc");
            form.setFieldsValue(data);
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            await client.put("/config/oidc", form.getFieldsValue());
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    return (
        <ConfigPageLayout title="OIDC Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={6}><Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item></Col>
                    <Col span={18}><Form.Item name="issuer" label="Issuer"><Input /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="client_id" label="Client ID"><Input.Password /></Form.Item></Col>
                    <Col span={12}><Form.Item name="audiences" label="Audiences"><Input /></Form.Item></Col>
                </Row>
                <Row gutter={16}>
                    <Col span={12}><Form.Item name="scopes" label="Scopes"><Input /></Form.Item></Col>
                    <Col span={12}><Form.Item name="claim_mappings" label="Claim Mappings"><Input /></Form.Item></Col>
                </Row>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
