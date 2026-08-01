import { useState, useEffect, useCallback } from "react";
import { Form, Select, Table, InputNumber, Button, Space, Input } from "antd";
import { PlusOutlined, DeleteOutlined } from "@ant-design/icons";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface CloudRoutingConfig {
    strategy: string;
    cloud_weights: Record<string, number>;
}

interface WeightRow {
    key: string;
    provider: string;
    weight: number;
}

export default function CloudRoutingConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();
    const [rows, setRows] = useState<WeightRow[]>([]);

    const toRows = (weights: Record<string, number>): WeightRow[] =>
        Object.entries(weights).map(([provider, weight]) => ({ key: provider, provider, weight }));

    const toWeights = (rs: WeightRow[]): Record<string, number> => {
        const m: Record<string, number> = {};
        rs.forEach((r) => { m[r.provider] = r.weight; });
        return m;
    };

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<CloudRoutingConfig>("/config/cloud-routing");
            form.setFieldsValue({ strategy: data.strategy });
            setRows(toRows(data.cloud_weights || {}));
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            await client.put("/config/cloud-routing", { strategy: values.strategy, cloud_weights: toWeights(rows) });
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    const addRow = () => {
        const newKey = `provider_${Date.now()}`;
        setRows([...rows, { key: newKey, provider: "", weight: 1 }]);
    };

    const removeRow = (key: string) => {
        setRows(rows.filter((r) => r.key !== key));
    };

    const updateRow = (key: string, field: "provider" | "weight", value: string | number) => {
        setRows(rows.map((r) => (r.key === key ? { ...r, [field]: value } : r)));
    };

    const columns = [
        {
            title: "Provider",
            dataIndex: "provider",
            render: (_: string, record: WeightRow) => (
                <Input
                    value={record.provider}
                    onChange={(e) => updateRow(record.key, "provider", e.target.value)}
                    placeholder="provider name"
                />
            ),
        },
        {
            title: "Weight",
            dataIndex: "weight",
            width: 150,
            render: (_: number, record: WeightRow) => (
                <InputNumber
                    min={0}
                    step={0.1}
                    value={record.weight}
                    onChange={(v) => updateRow(record.key, "weight", v ?? 1)}
                    style={{ width: "100%" }}
                />
            ),
        },
        {
            title: "Action",
            width: 80,
            render: (_: unknown, record: WeightRow) => (
                <Button icon={<DeleteOutlined />} danger size="small" onClick={() => removeRow(record.key)} />
            ),
        },
    ];

    return (
        <ConfigPageLayout title="Cloud Routing Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Form.Item name="strategy" label="Strategy">
                    <Select
                        options={[
                            { value: "round-robin", label: "Round Robin" },
                            { value: "weighted", label: "Weighted" },
                            { value: "random", label: "Random" },
                        ]}
                    />
                </Form.Item>
                <Form.Item label="Cloud Weights">
                    <Space direction="vertical" style={{ width: "100%" }}>
                        <Button icon={<PlusOutlined />} onClick={addRow} size="small">
                            Add Provider
                        </Button>
                        <Table
                            dataSource={rows}
                            columns={columns}
                            pagination={false}
                            size="small"
                            rowKey="key"
                        />
                    </Space>
                </Form.Item>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
