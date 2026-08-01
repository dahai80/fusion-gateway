import { useState, useEffect, useCallback } from "react";
import { Form, Switch, Select, InputNumber, Input, Row, Col, Table, Button, Space } from "antd";
import { PlusOutlined, DeleteOutlined } from "@ant-design/icons";
import client from "../../api/client";
import ConfigPageLayout, { useConfigNotifier } from "../../components/ConfigPageLayout";

interface ClusterNode {
    id: string;
    address: string;
    gpu: string;
    memory_gb: number;
}

interface ClusterConfig {
    enabled: boolean;
    mode: string;
    nodes: ClusterNode[];
    master_address: string;
    master_shared_token: string;
    load_balancer: string;
    health_check_interval: string;
    failure_threshold: number;
    recovery_interval: string;
}

interface NodeRow extends ClusterNode {
    key: string;
}

export default function ClusterConfig() {
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [form] = Form.useForm();
    const notifier = useConfigNotifier();
    const [nodes, setNodes] = useState<NodeRow[]>([]);

    const toRows = (ns: ClusterNode[]): NodeRow[] =>
        ns.map((n, i) => ({ ...n, key: n.id || `node_${i}_${Date.now()}` }));

    const fetch = useCallback(async () => {
        setLoading(true);
        try {
            const { data } = await client.get<ClusterConfig>("/config/cluster");
            form.setFieldsValue({
                enabled: data.enabled,
                mode: data.mode,
                master_address: data.master_address,
                master_shared_token: data.master_shared_token,
                load_balancer: data.load_balancer,
                health_check_interval: data.health_check_interval,
                failure_threshold: data.failure_threshold,
                recovery_interval: data.recovery_interval,
            });
            setNodes(toRows(data.nodes || []));
        } catch { notifier.error("Failed to load"); }
        setLoading(false);
    }, []);

    useEffect(() => { fetch(); }, [fetch]);

    const handleSave = async () => {
        try { await form.validateFields(); } catch { return; }
        setSaving(true);
        try {
            const values = form.getFieldsValue();
            const serialized = nodes.map(({ key, ...rest }) => rest);
            await client.put("/config/cluster", { ...values, nodes: serialized });
            notifier.success();
        } catch { notifier.error("Failed to save"); }
        setSaving(false);
    };

    const addNode = () => {
        setNodes([...nodes, { key: `node_${Date.now()}`, id: "", address: "", gpu: "", memory_gb: 0 }]);
    };

    const removeNode = (key: string) => {
        setNodes(nodes.filter((n) => n.key !== key));
    };

    const updateNode = (key: string, field: keyof ClusterNode, value: string | number) => {
        setNodes(nodes.map((n) => (n.key === key ? { ...n, [field]: value } : n)));
    };

    const columns = [
        {
            title: "ID",
            dataIndex: "id",
            render: (_: string, record: NodeRow) => (
                <Input value={record.id} onChange={(e) => updateNode(record.key, "id", e.target.value)} />
            ),
        },
        {
            title: "Address",
            dataIndex: "address",
            render: (_: string, record: NodeRow) => (
                <Input value={record.address} onChange={(e) => updateNode(record.key, "address", e.target.value)} />
            ),
        },
        {
            title: "GPU",
            dataIndex: "gpu",
            width: 120,
            render: (_: string, record: NodeRow) => (
                <Input value={record.gpu} onChange={(e) => updateNode(record.key, "gpu", e.target.value)} />
            ),
        },
        {
            title: "Memory (GB)",
            dataIndex: "memory_gb",
            width: 130,
            render: (_: number, record: NodeRow) => (
                <InputNumber min={0} value={record.memory_gb} onChange={(v) => updateNode(record.key, "memory_gb", v ?? 0)} style={{ width: "100%" }} />
            ),
        },
        {
            title: "Action",
            width: 80,
            render: (_: unknown, record: NodeRow) => (
                <Button icon={<DeleteOutlined />} danger size="small" onClick={() => removeNode(record.key)} />
            ),
        },
    ];

    return (
        <ConfigPageLayout title="Cluster Configuration" loading={loading} saving={saving} onSave={handleSave} onRefresh={fetch}>
            <Form form={form} layout="vertical">
                <Row gutter={16}>
                    <Col span={4}>
                        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
                            <Switch />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="mode" label="Mode">
                            <Select options={[{ value: "master" }, { value: "worker" }, { value: "standalone" }]} />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="master_address" label="Master Address">
                            <Input />
                        </Form.Item>
                    </Col>
                    <Col span={8}>
                        <Form.Item name="master_shared_token" label="Shared Token">
                            <Input.Password />
                        </Form.Item>
                    </Col>
                </Row>
                <Row gutter={16}>
                    <Col span={8}>
                        <Form.Item name="load_balancer" label="Load Balancer">
                            <Select options={[{ value: "round-robin" }, { value: "least-connections" }, { value: "random" }]} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="health_check_interval" label="Health Check Interval">
                            <Input placeholder="e.g. 10s" />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="failure_threshold" label="Failure Threshold">
                            <InputNumber min={0} style={{ width: "100%" }} />
                        </Form.Item>
                    </Col>
                    <Col span={4}>
                        <Form.Item name="recovery_interval" label="Recovery Interval">
                            <Input placeholder="e.g. 30s" />
                        </Form.Item>
                    </Col>
                </Row>
                <Form.Item label="Nodes">
                    <Space direction="vertical" style={{ width: "100%" }}>
                        <Button icon={<PlusOutlined />} onClick={addNode} size="small">
                            Add Node
                        </Button>
                        <Table dataSource={nodes} columns={columns} pagination={false} size="small" rowKey="key" />
                    </Space>
                </Form.Item>
            </Form>
            {notifier.ctx}
        </ConfigPageLayout>
    );
}
