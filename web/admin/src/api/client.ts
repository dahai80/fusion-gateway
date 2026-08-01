import axios from "axios";

const client = axios.create({
    baseURL: "/admin/api",
    timeout: 30000,
    headers: {
        "Content-Type": "application/json",
    },
});

client.interceptors.request.use(
    (config) => {
        const token = localStorage.getItem("admin_token");
        if (token) {
            config.headers.Authorization = `Bearer ${token}`;
        }
        return config;
    },
    (error) => {
        return Promise.reject(error);
    }
);

client.interceptors.response.use(
    (response) => {
        return response;
    },
    (error) => {
        if (error.response?.status === 401) {
            localStorage.removeItem("admin_token");
            window.location.href = "/admin/login";
        }
        return Promise.reject(error);
    }
);

export default client;
