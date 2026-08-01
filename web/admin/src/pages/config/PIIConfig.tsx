import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Select, Input, Table, Button, Space } from "antd";
import { PlusOutlined, DeleteOutlined } from "@ant-design/icons";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface PIIPattern {
    name: string;
    regex: string;
}

interface PIIConfig {
    enabled: boolean;
    action: string;
    patterns: PIIPattern[];
}

interface PatternRow extends PIIPattern {
    key: string;
}

export default function PIIConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();
    const [patterns, setPatterns] = useState<PatternRow[]>([]);

    const toRows = (ps: PIIPattern[]): PatternRow[] =>
        ps.map((p, i) => ({ ...p, key: `pattern_${i}_${Date.now()}` }));

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<PIIConfig>("/config/pii");
            form.setFieldsValue({ enabled: data.enabled, action: data.action });
            setPatterns(toRows(data.patterns || []));
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            const serialized = patterns.map(({ key, ...rest }) => rest);
            await client.put("/config/pii", { ...values, patterns: serialized });
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    const addPattern = () => {
        setPatterns([...patterns, { key: `pattern_${Date.now()}`, name: "", regex: "" }]);
    };

    const removePattern = (key: string) => {
        setPatterns(patterns.filter((p) => p.key !== key));
    };

    const updatePattern = (key: string, field: "name" | "regex", value: string) => {
        setPatterns(patterns.map((p) => (p.key === key ? { ...p, [field]: value } : p)));
    };

    const columns = [
        {
            title: "Name",
            dataIndex: "name",
            width: 200,
            render: (_: string, record: PatternRow) => (
                <Input value={record.name} onChange={(e) => updatePattern(record.key, "name", e.target.value)} />
            ),
        },
        {
            title: "Regex",
            dataIndex: "regex",
            render: (_: string, record: PatternRow) => (
                <Input value={record.regex} onChange={(e) => updatePattern(record.key, "regex", e.target.value)} />
            ),
        },
        {
            title: "Action",
            width: 80,
            render: (_: unknown, record: PatternRow) => (
                <Button icon={<DeleteOutlined />} danger size="small" onClick={() => removePattern(record.key)} />
            ),
        },
    ];

    return (
        <ConfigPageLayout title="PII Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Space size="large">
                    <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                        <Switch />
                    </Form.Item>
                    <Form.Item name="action" label="Action">
                        <Select
                            style={{ width: 200 }}
                            options={[{ value: "redact" }, { value: "block" }, { value: "log" }]}
                        />
                    </Form.Item>
                </Space>
                <Form.Item label="Patterns">
                    <Space direction="vertical" style={{ width: "100%" }}>
                        <Button icon={<PlusOutlined />} onClick={addPattern} size="small">
                            Add Pattern
                        </Button>
                        <Table dataSource={patterns} columns={columns} pagination={false} size="small" rowKey="key" />
                    </Space>
                </Form.Item>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
