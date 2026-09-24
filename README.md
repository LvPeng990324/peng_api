# peng_api

自用的 LLM API 分发网关。多个上游渠道统一成 OpenAI 格式出口，按优先级分发，失败自动降级，完整记录请求日志。

## 构建

go build -o peng_api .

（纯 Go，无 CGO；Windows/macOS/Linux 均可。）

## 运行

./peng_api -admin-password <管理密码>

| 参数 | 默认 | 说明 |
|---|---|---|
| -addr | :8080 | 监听地址 |
| -db | ./peng.db | SQLite 文件路径 |
| -admin-password | （必填） | 管理密码，也可用环境变量 PENG_ADMIN_PASSWORD |
| -log-retention-days | 30 | 日志保留天数 |
| -request-timeout | 300 | 非流式上游整体超时（秒） |
| -connect-timeout | 10 | 上游连接超时（秒） |
| -stream-first-byte-timeout | 60 | 流式首字节超时（秒） |

## 快速上手

1. 启动后用浏览器打开 http://localhost:8080 ，输入管理密码登录
2. 「渠道」页：新建渠道（地址、key、优先级），用「获取模型」从上游拉取并添加模型实体；给渠道选一个便宜的测试模型后可用「测试」按钮验证连通性
3. 「模型映射」页：新建标准名（如 glm-5.3，可带 context_length/max_output_tokens），绑定一个或多个渠道下的模型实体
4. 「Token」页：创建客户端 token
5. 客户端（任意 OpenAI 兼容工具）配置：base_url = http://localhost:8080/v1 ，api_key = 上一步的 token
6. 「日志」页查看每次请求的完整报文与降级链路

## 行为要点

- 客户端只需用 OpenAI 格式请求 /v1/chat/completions；模型名与标准名大小写敏感精确匹配
- 同一标准名绑定的多个模型按渠道优先级从大到小尝试；网络错误/超时/5xx/429 自动降级下一模型；4xx 原样透传（请求本身的问题）
- 渠道只能手动启停；禁用后其下模型实体不再参与分发，/v1/models 也不再展示相关标准名
- 流式（SSE）请求逐行透传，日志里存拼接后的完整响应
- GET /v1/models 返回带 context_length 的模型列表（Kimi 风格扩展字段）
