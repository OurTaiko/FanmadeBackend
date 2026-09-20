# 网站管理员与编辑权限

角色统一由 SSO 的 `ClientRole(user, client="fanmade", is_admin)` 管理。SSO 站点 staff/superuser 身份不会自动获得 Fanmade 管理员权限。旧用户导入时保留原本站管理员映射。

在 SSO 的 Django admin 管理指定用户的 ClientRole，或由有管理权限的人使用 `manage.py shell` 操作该模型。Fanmade 数据库已无 is_admin 列。

每次受保护请求实时读取 SSO 返回的本站角色，因此撤权在下一次请求生效。修改谱面允许上传者或本站管理员；删除仍仅允许上传者。浏览器操作仍要求 Origin 和 CSRF，任何客户端传入的角色声明都不被采信。后端 PostgreSQL 回归覆盖作者、普通用户、管理员、撤权及伪造角色。
