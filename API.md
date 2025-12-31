# 后端 API 协议（Supabase）

本项目通过 Supabase 客户端与后端交互。下面按功能列出所有与后端交互的协议格式、请求示例与响应字段说明。

> 使用说明：示例代码基于 `@supabase/supabase-js` 客户端（项目中为 `supabase` 实例）。

---

## 概览

- 数据表：`chat_sessions`, `messages`
- 存储桶（Storage）：`chat-media`
- 实时订阅：Postgres `INSERT` 事件（table: `messages`）

数据库表结构（摘自 `src/integrations/supabase/types.ts`）：

- `chat_sessions` Row:
  - `id: string`
  - `created_at: string`

- `messages` Row:
  - `id: string`
  - `session_id: string` (FK -> `chat_sessions.id`)
  - `content: string | null`
  - `message_type: string` (例如 `text` / `image` / `video`)
  - `file_url: string | null`
  - `sender_name: string | null`
  - `created_at: string`

---

## 1. 创建会话（Create chat session）

- 用途：创建新的聊天会话（无需额外字段，服务端生成 `id`）。
- Supabase 调用示例：

```ts
const { data, error } = await supabase
  .from('chat_sessions')
  .insert({})
  .select('id')
  .single();
```

- Insert 请求体：`{}`（可选 `id`、`created_at`，通常不提供）
- 成功响应示例（`data`）:

```json
{
  "id": "<session-id>",
  "created_at": "2025-12-31T12:34:56.789Z"
}
```

---

## 2. 验证会话存在（Check session exists）

- 用途：在进入聊天页面时确认 `sessionId` 有效。
- Supabase 调用示例：

```ts
const { data, error } = await supabase
  .from('chat_sessions')
  .select('id')
  .eq('id', sessionId)
  .single();
```

- 成功响应：`data` 为 `{ id, created_at }`；若不存在，`error` 或 `data` 为 `null`。

---

## 3. 加载消息（Load messages）

- 用途：获取指定会话的历史消息，按 `created_at` 升序排序。
- Supabase 调用示例：

```ts
const { data, error } = await supabase
  .from('messages')
  .select('*')
  .eq('session_id', sessionId)
  .order('created_at', { ascending: true });
```

- 成功响应：`data` 为 `Message[]`，其中 `Message` 字段见下。

Message 示例：

```json
{
  "id": "msg-uuid",
  "session_id": "session-uuid",
  "content": "Hello",
  "message_type": "text",
  "file_url": null,
  "sender_name": "Alice",
  "created_at": "2025-12-31T12:35:00.000Z"
}
```

---

## 4. 发送文本消息（Send text message）

- 用途：向 `messages` 表插入一条文本消息。
- Supabase 调用示例：

```ts
const { error } = await supabase.from('messages').insert({
  session_id: sessionId,
  content: content.trim(),
  message_type: 'text',
  sender_name: nickname,
});
```

- Insert 请求体（示例）:

```json
{
  "session_id": "session-uuid",
  "content": "Hello",
  "message_type": "text",
  "sender_name": "Alice"
}
```

- 成功响应：默认代码中未 .select() 返回插入行，若需要可在调用链加上 `.select('*')`。

---

## 5. 发送媒体（Upload media + create message）

流程：
1. 使用 `supabase.storage.from('chat-media').upload(path, file)` 上传文件。
2. 通过 `getPublicUrl(path)` 获取可访问的 URL。
3. 向 `messages` 插入一条包含 `file_url` 与 `message_type` 的记录。

示例代码：

```ts
// 1. 上传
const { error: uploadError } = await supabase.storage
  .from('chat-media')
  .upload(fileName, file);

// 2. 获取公开 URL
const { data: urlData } = supabase.storage
  .from('chat-media')
  .getPublicUrl(fileName);

// urlData.publicUrl 即文件访问地址

// 3. 插入消息记录
const { error: insertError } = await supabase.from('messages').insert({
  session_id: sessionId,
  message_type: messageType, // 'image' 或 'video'
  file_url: urlData.publicUrl,
  sender_name: nickname,
});
```

注意：`getPublicUrl` 的返回对象结构依赖 Supabase SDK 版本，常见为 `{ publicUrl: string }`。

---

## 6. WebSocket 实时通信 (Realtime)

本项目使用 Supabase Realtime 服务（基于 WebSocket）来实现消息的即时推送。

- **协议**: WebSocket (WSS)
- **底层机制**: Phoenix Channels
- **连接地址**: `wss://<project-ref>.supabase.co/realtime/v1/websocket?apikey=<anon-key>&vsn=1.0.0`

### 6.1 订阅配置

前端通过 Supabase SDK 建立 WebSocket 连接并订阅数据库变更。

- **Channel Topic**: `messages:<sessionId>` (应用层逻辑通道名)
- **监听类型**: `postgres_changes` (数据库变更事件)
- **订阅条件**:
  - Event: `INSERT` (仅监听新增行)
  - Schema: `public`
  - Table: `messages`
  - Filter: `session_id=eq.<sessionId>`

### 6.2 代码实现示例

```ts
// 1. 创建订阅通道
const channel = supabase
  .channel(`messages:${sessionId}`)
  .on(
    'postgres_changes',
    {
      event: 'INSERT',
      schema: 'public',
      table: 'messages',
      filter: `session_id=eq.${sessionId}`,
    },
    (payload) => {
      // payload.new 即为新插入的消息数据
      console.log('New message received:', payload.new);
    }
  )
  .subscribe((status) => {
    if (status === 'SUBSCRIBED') {
      console.log('WebSocket connected');
    }
  });

// 2. 取消订阅（组件卸载时）
supabase.removeChannel(channel);
```

### 6.3 消息推送负载 (Payload)

当 `messages` 表有新记录插入时，WebSocket 接收到的数据结构（`payload`）如下：

```json
{
  "schema": "public",
  "table": "messages",
  "commit_timestamp": "2025-12-31T12:35:00.000Z",
  "eventType": "INSERT",
  "new": {
    "id": "msg-uuid",
    "session_id": "session-uuid",
    "content": "Hello World",
    "message_type": "text",
    "file_url": null,
    "sender_name": "Alice",
    "created_at": "2025-12-31T12:35:00.000Z"
  },
  "old": {},
  "errors": null
}
```

> 注意：客户端必须保持 WebSocket 连接活跃（Supabase SDK 会自动处理心跳 Heartbeat）。

---

## 7. 错误与响应规范

- Supabase JS 调用通常返回 `{ data, error }` 或 `{ data, error, status }`。
- 成功：`error === null`，`data` 为查询/插入结果（或 `[]`）。
- 失败：`error` 包含 `message` 与 `details`（视 SDK 与后端设置而定）。前端逻辑中需检查 `error` 并提示用户。

---

## 8. 参考（项目位置）

- Supabase 客户端实例： `src/integrations/supabase/client.ts`
- Supabase 类型定义（表结构）： `src/integrations/supabase/types.ts`
- 主要调用点：
  - 创建会话与跳转： `src/pages/Index.tsx`
  - 加载/发送消息、上传媒体、实时订阅： `src/pages/Chat.tsx`

---

如果你需要，我可以：

- 按 REST 风格或 OpenAPI 生成更正式的 API 描述（YAML/JSON）；
- 为每个请求增加示例请求/响应的 curl 或 PostgREST URL；
- 将 `API.md` 中的字段自动同步到 README 或代码注释中。

---

文件末。
