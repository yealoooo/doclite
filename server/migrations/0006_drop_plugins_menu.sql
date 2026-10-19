-- M10-7：去掉"系统管理 › 插件能力"菜单（用户 2026-10-19）。0001 的初始数据带了它，但没有插件功能，界面上只是一个
-- "建设中"的占位页，客户能看到。角色的授权（role_menus）随外键 ON DELETE CASCADE 一起删掉。
DELETE FROM menus WHERE menu_code = 'system.plugins';
