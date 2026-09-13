# 网站管理员与编辑权限

迁移 008 新增 `users.is_admin boolean NOT NULL DEFAULT false`。已有用户和新注册用户都保持普通身份，不自动把第一个账号或演示账号设为管理员。

编辑作品信息的条件为：已登录，且 `currentUser.id == chart.owner_id` 或数据库记录的 `is_admin == true`。这里的作者指拥有作品的上传账号，不是 TJA 内可填写的 MAKER 字符串。管理员也需要正确 Origin 和 CSRF。删除接口仍维持原有作者权限，本次仅扩展编辑信息权限。

登录／注册／GET /me 的 user 对象增加 isAdmin 布尔值，供前端显示编辑入口；每次后端鉴权都重新从数据库读取角色，因此撤权后不必等待 Cookie 过期。注册和编辑请求不接受 isAdmin、role 等额外字段。

需要设置管理员时，先在本机数据库确认目标账号，再由有数据库管理权限的人执行（将占位用户名替换为真实账号）：

```sql
SELECT id, username, is_admin FROM users WHERE username = '目标用户名';
UPDATE users SET is_admin = true WHERE username = '目标用户名'
RETURNING id, username, is_admin;
```

撤销时将 true 改为 false。用户刷新页面或重新登录后，前端读取最新角色；后端限制从下一次请求立即生效。当前没有公开的角色修改 API，也没有管理员账号管理界面。

验证：临时 PostgreSQL schema 内覆盖作者成功、普通他人 403、管理员成功、管理员缺少 CSRF 403、撤销管理员后旧 Session 403、注册／请求伪造 isAdmin 被拒绝。没有修改任何现有演示账号的角色。
