import axios from "axios";

const client = axios.create({
    baseURL: "/admin/api",
    timeout: 30000,
    withCredentials: true,
    headers: {
        "Content-Type": "application/json",
    },
});

client.interceptors.request.use(
    (config) => {
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
            localStorage.removeItem("admin_logged_in");
            window.location.href = "/admin/login";
        }
        return Promise.reject(error);
    }
);

export default client;
