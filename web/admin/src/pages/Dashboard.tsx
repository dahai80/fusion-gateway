import { useState, useEffect } from "react";
import { Row, Col, Card, Statistic, Spin, Typography } from "antd";
import {
    ApiOutlined,
    DollarOutlined,
    ThunderboltOutlined,
    PercentageOutlined,
} from "@ant-design/icons";
import {
    LineChart,
    Line,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip,
    ResponsiveContainer,
    PieChart,
    Pie,
    Cell,
    Legend,
} from "recharts";
import client from "../api/client";

const { Title } = Typography;

interface DashboardData {
    total_requests: number;
    total_tokens: number;
    total_cost: number;
    local_hit_rate: number;
    cost_trend: { date: string; cost: number }[];
    model_distribution: { name: string; value: number }[];
}

const PIE_COLORS = ["#1677ff", "#52c41a", "#faad14", "#ff4d4f", "#722ed1", "#13c2c2", "#eb2f96"];

export default function Dashboard() {
    const [loading, setLoading] = useState(true);
    const [data, setData] = useState<DashboardData>({
        total_requests: 0,
        total_tokens: 0,
        total_cost: 0,
        local_hit_rate: 0,
        cost_trend: [],
        model_distribution: [],
    });

    useEffect(() => {
        fetchDashboard();
    }, []);

    const fetchDashboard = async () => {
        setLoading(true);
        try {
            const res = await client.get("/dashboard");
            setData(res.data?.data || res.data || data);
        } catch (err) {
            console.error("Failed to fetch dashboard data:", err);
        } finally {
            setLoading(false);
        }
    };

    if (loading) {
        return (
            <div style={{ textAlign: "center", padding: 80 }}>
                <Spin size="large" />
            </div>
        );
    }

    return (
        <div>
            <Title level={4} style={{ marginBottom: 24 }}>Dashboard Overview</Title>
            <Row gutter={[16, 16]}>
                <Col xs={24} sm={12} lg={6}>
                    <Card bordered={false}>
                        <Statistic
                            title="Total Requests"
                            value={data.total_requests}
                            prefix={<ApiOutlined />}
                            valueStyle={{ color: "#1677ff" }}
                        />
                    </Card>
                </Col>
                <Col xs={24} sm={12} lg={6}>
                    <Card bordered={false}>
                        <Statistic
                            title="Total Tokens"
                            value={data.total_tokens}
                            prefix={<ThunderboltOutlined />}
                            valueStyle={{ color: "#52c41a" }}
                        />
                    </Card>
                </Col>
                <Col xs={24} sm={12} lg={6}>
                    <Card bordered={false}>
                        <Statistic
                            title="Total Cost"
                            value={data.total_cost}
                            prefix={<DollarOutlined />}
                            precision={4}
                            valueStyle={{ color: "#faad14" }}
                        />
                    </Card>
                </Col>
                <Col xs={24} sm={12} lg={6}>
                    <Card bordered={false}>
                        <Statistic
                            title="Local Hit Rate"
                            value={data.local_hit_rate}
                            prefix={<PercentageOutlined />}
                            suffix="%"
                            precision={1}
                            valueStyle={{ color: "#722ed1" }}
                        />
                    </Card>
                </Col>
            </Row>

            <Row gutter={[16, 16]} style={{ marginTop: 24 }}>
                <Col xs={24} lg={16}>
                    <Card title="Cost Trend (7 Days)" bordered={false}>
                        <ResponsiveContainer width="100%" height={320}>
                            <LineChart data={data.cost_trend}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="date" />
                                <YAxis />
                                <Tooltip />
                                <Line type="monotone" dataKey="cost" stroke="#1677ff" strokeWidth={2} dot={{ r: 4 }} />
                            </LineChart>
                        </ResponsiveContainer>
                    </Card>
                </Col>
                <Col xs={24} lg={8}>
                    <Card title="Model Distribution" bordered={false}>
                        <ResponsiveContainer width="100%" height={320}>
                            <PieChart>
                                <Pie
                                    data={data.model_distribution}
                                    cx="50%"
                                    cy="50%"
                                    innerRadius={60}
                                    outerRadius={100}
                                    paddingAngle={2}
                                    dataKey="value"
                                    label={({ name, percent }) => `${name} ${(percent * 100).toFixed(0)}%`}
                                >
                                    {data.model_distribution.map((_entry, index) => (
                                        <Cell key={`cell-${index}`} fill={PIE_COLORS[index % PIE_COLORS.length]} />
                                    ))}
                                </Pie>
                                <Tooltip />
                                <Legend />
                            </PieChart>
                        </ResponsiveContainer>
                    </Card>
                </Col>
            </Row>
        </div>
    );
}
